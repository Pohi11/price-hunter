package demostore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

var at = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestPricesAreDeterministicAndBounded(t *testing.T) {
	for _, p := range Catalog() {
		a, b := p.PriceAt(at), p.PriceAt(at)
		if a != b {
			t.Fatalf("%s: non-deterministic price", p.Slug)
		}
		for h := 0; h < 24*14; h++ {
			m := p.PriceAt(at.Add(time.Duration(h) * time.Hour))
			if p.Fault == FaultGlitch {
				continue
			}
			lo := float64(p.Base) * (1 - p.Amplitude) * 0.98
			hi := float64(p.Base) * (1 + p.Amplitude) * 1.02
			if float64(m.Minor) < lo || float64(m.Minor) > hi {
				t.Fatalf("%s at +%dh: %d outside [%v, %v]", p.Slug, h, m.Minor, lo, hi)
			}
		}
	}
}

func TestGlitchWindow(t *testing.T) {
	p, _ := Find("glitch-tv")
	normal := p.PriceAt(time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC))
	glitch := p.PriceAt(time.Date(2026, 10, 1, 3, 10, 0, 0, time.UTC))
	if glitch.Minor*50 > normal.Minor {
		t.Fatalf("glitch price %d not dramatically below %d", glitch.Minor, normal.Minor)
	}
}

func get(t *testing.T, s *Server, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://store.test"+path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestFaultStatusCodes(t *testing.T) {
	s := New(Options{Now: func() time.Time { return at }, SlowDelay: time.Millisecond})
	tests := map[string]int{
		"/p/air-fryer":          200,
		"/p/discontinued-mixer": 410,
		"/p/blocked-fan":        403,
		"/p/ratelimited-heater": 429,
		"/p/broken-toaster":     200,
		"/p/slow-kettle":        200,
		"/p/nope":               404,
		"/robots.txt":           200,
	}
	for path, want := range tests {
		if got := get(t, s, path).Code; got != want {
			t.Errorf("%s: status %d, want %d", path, got, want)
		}
	}
	if rec := get(t, s, "/p/ratelimited-heater"); rec.Header().Get("Retry-After") != "120" {
		t.Error("missing Retry-After")
	}
}

func TestLayouts(t *testing.T) {
	s := New(Options{Now: func() time.Time { return at }})
	checks := map[string]string{
		"/p/air-fryer":        `application/ld+json`,
		"/p/smartphone-x":     `product:price:amount`,
		"/p/sedan-se":         `itemprop="price"`,
		"/p/espresso-machine": `data-testid="price"`,
	}
	for path, needle := range checks {
		body := get(t, s, path).Body.String()
		if !strings.Contains(body, needle) {
			t.Errorf("%s: missing %q", path, needle)
		}
	}
	if body := get(t, s, "/p/espresso-machine").Body.String(); strings.Contains(body, "ld+json") {
		t.Error("selectors layout must not include structured data")
	}
}

func TestAdminOverrides(t *testing.T) {
	s := New(Options{Now: func() time.Time { return at }, AdminToken: "secret"})
	put := func(token, body string) int {
		req := httptest.NewRequest(http.MethodPut, "/admin/products/air-fryer", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := put("wrong", `{"fault":"error"}`); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	if code := put("secret", `{"fault":"error"}`); code != 204 {
		t.Fatalf("set override: %d", code)
	}
	if code := get(t, s, "/p/air-fryer").Code; code != 500 {
		t.Fatalf("override not applied: %d", code)
	}
	if code := put("secret", `{"fault":"none","price":"79.99"}`); code != 204 {
		t.Fatalf("set price: %d", code)
	}
	if body := get(t, s, "/p/air-fryer").Body.String(); !strings.Contains(body, "$79.99") {
		t.Fatal("price override not rendered")
	}

	disabled := New(Options{})
	req := httptest.NewRequest(http.MethodPut, "/admin/products/air-fryer", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	disabled.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("admin should be disabled without a token, got %d", rec.Code)
	}
}

func TestDisplay(t *testing.T) {
	for minor, want := range map[int64]string{9400: "$94.00", 129999: "$1,299.99", 2540000: "$25,400.00", 99: "$0.99"} {
		if got := display(domain.Money{Minor: minor, Currency: "USD"}); got != want {
			t.Errorf("display(%d) = %q, want %q", minor, got, want)
		}
	}
}
