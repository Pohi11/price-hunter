package retailer

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/demostore"
	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/extract"
	"github.com/Pohi11/price-hunter/internal/fetch"
)

var update = flag.Bool("update", false, "rewrite golden files")

type golden struct {
	Price        string `json:"price,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Availability string `json:"availability,omitempty"`
	Strategy     string `json:"strategy,omitempty"`
	Title        string `json:"title,omitempty"`
	Outcome      string `json:"outcome"`
}

// TestGoldenPages runs every saved page under testdata/pages/<profile>/
// through that profile's pipeline and compares with <page>.golden.json.
// When a real page stops parsing, save it here (the S3 snapshot), fix the
// profile, and regenerate with: go test ./internal/retailer -run Golden -update
func TestGoldenPages(t *testing.T) {
	reg, err := Default(Options{AllowGeneric: true})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "testdata", "pages")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		profile := reg.byID(d.Name())
		if profile == nil {
			t.Fatalf("testdata/pages/%s: no profile with that id", d.Name())
		}
		pages, _ := filepath.Glob(filepath.Join(root, d.Name(), "*.html"))
		for _, page := range pages {
			count++
			name := d.Name() + "/" + filepath.Base(page)
			t.Run(name, func(t *testing.T) {
				body, err := os.ReadFile(page)
				if err != nil {
					t.Fatal(err)
				}
				doc, err := extract.Parse(body, "https://shop.example.com/p/x")
				if err != nil {
					t.Fatal(err)
				}
				res, err := profile.Pipeline().Run(doc, profile.Currency)
				outcome, _ := Classify(err)
				got := golden{Outcome: string(outcome)}
				if err == nil {
					got.Price = res.Offer.Price.Decimal()
					got.Currency = string(res.Offer.Price.Currency)
					got.Availability = string(res.Offer.Availability)
					got.Strategy = res.Offer.Strategy
					got.Title = res.Offer.Title
				}
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				goldenPath := strings.TrimSuffix(page, ".html") + ".golden.json"
				if *update {
					if err := os.WriteFile(goldenPath, append(gotJSON, '\n'), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(goldenPath)
				if err != nil {
					t.Fatalf("missing golden file (run with -update): %v", err)
				}
				if strings.TrimSpace(string(want)) != strings.TrimSpace(string(gotJSON)) {
					t.Fatalf("extraction changed.\n got: %s\nwant: %s", gotJSON, want)
				}
			})
		}
	}
	if count == 0 {
		t.Fatal("no golden pages found")
	}
}

func TestLookup(t *testing.T) {
	reg, err := Default(Options{AllowGeneric: false, ExtraDomains: map[string][]string{"demostore": {"abc.lambda-url.us-east-1.on.aws"}}})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{
		"books.toscrape.com":              "books-toscrape",
		"WWW.Books.ToScrape.com":          "books-toscrape",
		"localhost:8081":                  "demostore",
		"abc.lambda-url.us-east-1.on.aws": "demostore",
	} {
		p, err := reg.Lookup(host)
		if err != nil || p.ID != want {
			t.Errorf("Lookup(%q) = %v, %v; want %s", host, p, err, want)
		}
	}
	if _, err := reg.Lookup("unknown-shop.com"); !errors.Is(err, domain.ErrUnsupportedRetailer) {
		t.Fatalf("allowlist mode: err = %v", err)
	}

	open, _ := Default(Options{AllowGeneric: true})
	if p, err := open.Lookup("unknown-shop.com"); err != nil || p.ID != GenericID {
		t.Fatalf("generic fallback: %v %v", p, err)
	}
	if open.RateFor("books.toscrape.com") != 0.5 {
		t.Fatalf("RateFor = %v", open.RateFor("books.toscrape.com"))
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	bad := map[string]string{
		"unknown strategy":   "generic: {strategies: [jsonld]}\nretailers: [{id: a, domains: [a.com], strategies: [magic]}]",
		"selectors no price": "generic: {strategies: [jsonld]}\nretailers: [{id: a, domains: [a.com], strategies: [selectors]}]",
		"duplicate domain":   "generic: {strategies: [jsonld]}\nretailers: [{id: a, domains: [x.com], strategies: [jsonld]}, {id: b, domains: [x.com], strategies: [jsonld]}]",
		"bad currency":       "generic: {strategies: [jsonld]}\nretailers: [{id: a, domains: [a.com], currency: XXX, strategies: [jsonld]}]",
		"reserved id":        "generic: {strategies: [jsonld]}\nretailers: [{id: generic, domains: [a.com], strategies: [jsonld]}]",
	}
	for name, doc := range bad {
		if _, err := Load([]byte(doc), Options{}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// TestSourceAgainstDemoStore exercises fetch + registry + extraction end to
// end against an in-process demo store, including its failure modes.
func TestSourceAgainstDemoStore(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 7, 0, 0, time.UTC) // minute 7: flaky blender is up
	store := httptest.NewServer(demostore.New(demostore.Options{Now: func() time.Time { return now }, SlowDelay: 3 * time.Second}))
	defer store.Close()

	reg, err := Default(Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := fetch.New(fetch.Options{
		Guard:         fetch.Guard{AllowLoopback: true},
		Timeout:       time.Second,
		MaxRetries:    -1,
		RespectRobots: true,
		HostRate:      reg.RateFor,
	})
	src := NewSource(client, reg)

	tests := []struct {
		slug     string
		outcome  domain.OutcomeCode
		strategy string
	}{
		{"air-fryer", domain.OutcomeOK, "jsonld"},
		{"smartphone-x", domain.OutcomeOK, "meta"},
		{"sedan-se", domain.OutcomeOK, "meta"}, // microdata is handled by the meta extractor
		{"espresso-machine", domain.OutcomeOK, "selectors"},
		{"flaky-blender", domain.OutcomeOK, "jsonld"},
		{"broken-toaster", domain.OutcomePriceNotFound, ""},
		{"discontinued-mixer", domain.OutcomeNotFound, ""},
		{"blocked-fan", domain.OutcomeBlocked, ""},
		{"ratelimited-heater", domain.OutcomeRateLimited, ""},
		{"slow-kettle", domain.OutcomeFetchTransient, ""},
	}
	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			q, err := src.Quote(context.Background(), store.URL+"/p/"+tt.slug)
			outcome, retryAfter := Classify(err)
			if outcome != tt.outcome {
				t.Fatalf("outcome = %s (%v), want %s", outcome, err, tt.outcome)
			}
			if q.Retailer != "demostore" {
				t.Fatalf("retailer = %q", q.Retailer)
			}
			if tt.outcome == domain.OutcomeOK {
				p, _ := demostore.Find(tt.slug)
				if q.Offer.Price != p.PriceAt(now) || q.Offer.Strategy != tt.strategy {
					t.Fatalf("offer = %+v, want %v via %s", q.Offer, p.PriceAt(now), tt.strategy)
				}
			}
			if tt.outcome == domain.OutcomeRateLimited && retryAfter != 2*time.Minute {
				t.Fatalf("retry-after = %v", retryAfter)
			}
			if tt.outcome == domain.OutcomePriceNotFound && len(q.Body) == 0 {
				t.Fatal("body must be kept for snapshotting")
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		err  error
		want domain.OutcomeCode
	}{
		{nil, domain.OutcomeOK},
		{&fetch.Error{Kind: fetch.KindForbidden}, domain.OutcomeForbiddenTarget},
		{&fetch.Error{Kind: fetch.KindDisallowed}, domain.OutcomeRobotsDisallowed},
		{&fetch.Error{Kind: fetch.KindTooLarge}, domain.OutcomeTooLarge},
		{&fetch.Error{Kind: fetch.KindUnsupported}, domain.OutcomeUnsupportedContent},
		{&fetch.Error{Kind: fetch.KindHTTP}, domain.OutcomeHTTPError},
		{extract.ErrAmbiguous, domain.OutcomeAmbiguousPrice},
		{extract.ErrImplausible, domain.OutcomeImplausiblePrice},
		{domain.ErrUnsupportedRetailer, domain.OutcomeForbiddenTarget},
		{context.DeadlineExceeded, domain.OutcomeFetchTransient},
	}
	for _, tt := range tests {
		if got, _ := Classify(tt.err); got != tt.want {
			t.Errorf("Classify(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}
