//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"github.com/Pohi11/price-hunter/internal/api"
	"github.com/Pohi11/price-hunter/internal/auth"
	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/products"
	"github.com/Pohi11/price-hunter/internal/retailer"
	"github.com/Pohi11/price-hunter/internal/store"
	"github.com/Pohi11/price-hunter/internal/testutil"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type fakeQueue struct {
	mu   sync.Mutex
	msgs []domain.CheckMessage
}

func (q *fakeQueue) Enqueue(_ context.Context, msgs ...domain.CheckMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, msgs...)
	return nil
}

type harness struct {
	t       *testing.T
	handler http.Handler
	store   *store.Store
	queue   *fakeQueue
	router  routers.Router
	clock   *time.Time
	deleted []string
}

func newHarness(t *testing.T, maxProducts int) *harness {
	t.Helper()
	db, table := testutil.NewTable(t)
	st := store.New(db, table)
	reg, err := retailer.Default(retailer.Options{AllowGeneric: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, store: st, queue: &fakeQueue{}}
	now := t0
	h.clock = &now
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := products.New(st, h.queue, reg, products.Config{MaxProducts: maxProducts, ManualCooldown: 5 * time.Minute}, log)
	svc.SetClock(func() time.Time { return *h.clock }, nil)
	svc.OnDeleted = func(_ context.Context, id string) { h.deleted = append(h.deleted, id) }
	h.handler = api.New(svc, reg, log, api.Options{
		Auth: auth.Dev("alice", "alice@example.com"), MaxProducts: maxProducts, Version: "test",
		Now: func() time.Time { return *h.clock },
	})

	doc, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("spec invalid: %v", err)
	}
	h.router, err = legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type response struct {
	*httptest.ResponseRecorder
	body map[string]any
}

// do sends a request and validates the response against the OpenAPI spec.
func (h *harness) do(method, path string, body any, headers ...string) response {
	h.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "http://localhost:8088"+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	route, params, err := h.router.FindRoute(req)
	if err == nil {
		in := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
				Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}},
			Status: rec.Code, Header: rec.Header(), Body: io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
			Options: &openapi3filter.Options{IncludeResponseStatus: true},
		}
		if verr := openapi3filter.ValidateResponse(context.Background(), in); verr != nil {
			h.t.Fatalf("%s %s -> %d violates the OpenAPI spec: %v\nbody: %s", method, path, rec.Code, verr, rec.Body.String())
		}
	}
	res := response{ResponseRecorder: rec}
	if strings.Contains(rec.Header().Get("Content-Type"), "json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &res.body)
	}
	return res
}

func (r response) str(path ...string) string {
	var v any = r.body
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[p]
	}
	s, _ := v.(string)
	return s
}

func expect(t *testing.T, r response, status int) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", r.Code, status, r.Body.String())
	}
}

func fieldCodes(r response) map[string]string {
	out := map[string]string{}
	errs, _ := r.body["errors"].([]any)
	for _, e := range errs {
		m := e.(map[string]any)
		out[m["field"].(string)] = m["code"].(string)
	}
	return out
}

var airFryer = map[string]any{
	"name": "Ninja Air Fryer", "url": "https://shop.example.com/p/ninja-af101?utm_source=newsletter",
	"target_price": map[string]string{"amount": "100.00", "currency": "USD"}, "check_frequency": "6h",
}

func TestProductLifecycle(t *testing.T) {
	h := newHarness(t, 10)

	created := h.do("POST", "/v1/products", airFryer)
	expect(t, created, http.StatusCreated)
	id := created.str("id")
	if created.Header().Get("Location") != "/v1/products/"+id || created.Header().Get("ETag") != `W/"1"` {
		t.Fatalf("headers: %v", created.Header())
	}
	if created.str("url") != "https://shop.example.com/p/ninja-af101" {
		t.Fatalf("tracking params not stripped: %s", created.str("url"))
	}
	if created.str("latest_check", "status") != "QUEUED" || len(h.queue.msgs) != 1 || h.queue.msgs[0].Trigger != domain.TriggerInitial {
		t.Fatalf("first check not queued: %+v", h.queue.msgs)
	}

	expect(t, h.do("GET", "/v1/products/"+id, nil), http.StatusOK)
	list := h.do("GET", "/v1/products", nil)
	expect(t, list, http.StatusOK)
	if items := list.body["items"].([]any); len(items) != 1 {
		t.Fatalf("list = %d items", len(items))
	}

	// Rename with the right ETag, then a stale ETag is rejected.
	renamed := h.do("PATCH", "/v1/products/"+id, map[string]any{"name": "Air Fryer XL"}, "If-Match", `W/"1"`)
	expect(t, renamed, http.StatusOK)
	if renamed.str("name") != "Air Fryer XL" || renamed.body["version"].(float64) != 2 {
		t.Fatalf("rename: %s", renamed.Body.String())
	}
	expect(t, h.do("PATCH", "/v1/products/"+id, map[string]any{"name": "x"}, "If-Match", `W/"1"`), http.StatusPreconditionFailed)

	paused := h.do("PATCH", "/v1/products/"+id, map[string]any{"paused": true})
	expect(t, paused, http.StatusOK)
	if paused.str("status") != "PAUSED" || paused.body["next_check_at"] != nil {
		t.Fatalf("pause: %s", paused.Body.String())
	}
	resumed := h.do("PATCH", "/v1/products/"+id, map[string]any{"paused": false})
	if resumed.str("status") != "ACTIVE" || resumed.body["next_check_at"] == nil {
		t.Fatalf("resume: %s", resumed.Body.String())
	}
	bad := h.do("PATCH", "/v1/products/"+id, map[string]any{"target_price": map[string]string{"amount": "90", "currency": "EUR"}})
	expect(t, bad, http.StatusUnprocessableEntity)
	if fieldCodes(bad)["target_price.currency"] != "CURRENCY_MISMATCH" {
		t.Fatalf("currency change: %v", fieldCodes(bad))
	}

	expect(t, h.do("DELETE", "/v1/products/"+id, nil), http.StatusNoContent)
	expect(t, h.do("GET", "/v1/products/"+id, nil), http.StatusNotFound)
	expect(t, h.do("DELETE", "/v1/products/"+id, nil), http.StatusNotFound)
	if len(h.deleted) != 1 || h.deleted[0] != id {
		t.Fatalf("OnDeleted hook: %v", h.deleted)
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t, 10)
	r := h.do("POST", "/v1/products", map[string]any{
		"name": "", "url": "http://169.254.169.254/latest/meta-data/",
		"target_price": map[string]string{"amount": "-5", "currency": "USD"}, "check_frequency": "5m",
	})
	expect(t, r, http.StatusUnprocessableEntity)
	codes := fieldCodes(r)
	want := map[string]string{"name": "REQUIRED", "url": "IP_LITERAL_NOT_ALLOWED", "target_price.amount": "INVALID", "check_frequency": "INVALID"}
	for f, c := range want {
		if codes[f] != c {
			t.Errorf("field %s: code %q, want %q (all: %v)", f, codes[f], c, codes)
		}
	}

	r = h.do("POST", "/v1/products", map[string]any{
		"name": "Book", "url": "https://books.toscrape.com/catalogue/x/index.html",
		"target_price": map[string]string{"amount": "20.00", "currency": "USD"},
	})
	if fieldCodes(r)["target_price.currency"] != "CURRENCY_MISMATCH" {
		t.Fatalf("GBP retailer with USD target: %s", r.Body.String())
	}
	// Omitting the currency uses the retailer's.
	r = h.do("POST", "/v1/products", map[string]any{
		"name": "Book", "url": "https://books.toscrape.com/catalogue/x/index.html",
		"target_price": map[string]string{"amount": "20.00", "currency": ""},
	})
	expect(t, r, http.StatusCreated)
	if r.str("target_price", "currency") != "GBP" {
		t.Fatalf("currency default: %s", r.Body.String())
	}

	expect(t, h.do("POST", "/v1/products", `{"name":"x","bogus":1}`), http.StatusBadRequest)
	expect(t, h.do("POST", "/v1/products", `{"name":`), http.StatusBadRequest)
	expect(t, h.do("POST", "/v1/products", `{}{}`), http.StatusBadRequest)
	expect(t, h.do("POST", "/v1/products", `{"name":"`+strings.Repeat("a", 20000)+`"}`), http.StatusRequestEntityTooLarge)
	expect(t, h.do("POST", "/v1/products", `{}`, "Content-Type", "text/plain"), http.StatusUnsupportedMediaType)
}

func TestDuplicateAndQuota(t *testing.T) {
	h := newHarness(t, 2)
	first := h.do("POST", "/v1/products", airFryer)
	expect(t, first, http.StatusCreated)

	dup := map[string]any{}
	for k, v := range airFryer {
		dup[k] = v
	}
	dup["url"] = "https://SHOP.example.com/p/ninja-af101#reviews" // same canonical URL
	r := h.do("POST", "/v1/products", dup)
	expect(t, r, http.StatusConflict)
	if r.str("existing_product_id") != first.str("id") {
		t.Fatalf("duplicate body: %s", r.Body.String())
	}

	other := map[string]any{}
	for k, v := range airFryer {
		other[k] = v
	}
	other["url"] = "https://shop.example.com/p/other"
	expect(t, h.do("POST", "/v1/products", other), http.StatusCreated)
	other["url"] = "https://shop.example.com/p/third"
	expect(t, h.do("POST", "/v1/products", other), http.StatusForbidden)
}

func TestOwnershipIsolation(t *testing.T) {
	h := newHarness(t, 10)
	id := h.do("POST", "/v1/products", airFryer).str("id")
	bob := []string{"X-Dev-User", "bob"}
	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/products/" + id},
		{"DELETE", "/v1/products/" + id},
		{"GET", "/v1/products/" + id + "/prices"},
		{"POST", "/v1/products/" + id + "/checks"},
		{"GET", "/v1/products/" + id + "/checks"},
	} {
		r := h.do(req.method, req.path, nil, bob...)
		if r.Code != http.StatusNotFound {
			t.Errorf("bob %s %s = %d, want 404", req.method, req.path, r.Code)
		}
	}
	var patch any = map[string]any{"name": "pwned"}
	expect(t, h.do("PATCH", "/v1/products/"+id, patch, bob...), http.StatusNotFound)
	if items := h.do("GET", "/v1/products", nil, bob...).body["items"].([]any); len(items) != 0 {
		t.Fatal("bob sees alice's products")
	}
	expect(t, h.do("GET", "/v1/products", nil, "X-Dev-User", "not valid!"), http.StatusUnauthorized)
}

func TestPriceHistory(t *testing.T) {
	h := newHarness(t, 10)
	id := h.do("POST", "/v1/products", airFryer).str("id")
	var pts []domain.PricePoint
	for i := 0; i < 240; i++ { // 10 days hourly
		minor := int64(12000 - i*10)
		if i == 100 {
			minor = 5000 // one deep dip that downsampling must keep
		}
		pts = append(pts, domain.PricePoint{ProductID: id, ObservedAt: t0.Add(-240*time.Hour + time.Duration(i)*time.Hour),
			Price: domain.Money{Minor: minor, Currency: "USD"}, Availability: domain.InStock, Strategy: "jsonld"})
	}
	if err := h.store.PutPricePoints(context.Background(), pts); err != nil {
		t.Fatal(err)
	}

	raw := h.do("GET", "/v1/products/"+id+"/prices?limit=50", nil)
	expect(t, raw, http.StatusOK)
	if n := len(raw.body["points"].([]any)); n != 50 || raw.str("next_cursor") == "" {
		t.Fatalf("raw page: %d points, cursor %q", n, raw.str("next_cursor"))
	}
	next := h.do("GET", "/v1/products/"+id+"/prices?limit=50&cursor="+raw.str("next_cursor"), nil)
	expect(t, next, http.StatusOK)

	ds := h.do("GET", "/v1/products/"+id+"/prices?max_points=40", nil)
	expect(t, ds, http.StatusOK)
	points := ds.body["points"].([]any)
	if ds.str("resolution") != "downsampled" || len(points) > 40 {
		t.Fatalf("downsampled: %s with %d points", ds.str("resolution"), len(points))
	}
	if ds.str("summary", "min") != "50.00" {
		t.Fatalf("summary min = %s", ds.str("summary", "min"))
	}
	keptDip := false
	for _, p := range points {
		if p.(map[string]any)["price"] == "50.00" {
			keptDip = true
		}
	}
	if !keptDip {
		t.Fatal("downsampling dropped the lowest price")
	}

	expect(t, h.do("GET", "/v1/products/"+id+"/prices?from=yesterday", nil), http.StatusBadRequest)
	expect(t, h.do("GET", "/v1/products/"+id+"/prices?from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z", nil), http.StatusUnprocessableEntity)
	expect(t, h.do("GET", "/v1/products/"+id+"/prices?cursor=forged", nil), http.StatusBadRequest)
}

func TestManualChecks(t *testing.T) {
	h := newHarness(t, 10)
	id := h.do("POST", "/v1/products", airFryer).str("id")

	r := h.do("POST", "/v1/products/"+id+"/checks", nil)
	expect(t, r, http.StatusAccepted)
	checkID := r.str("check_id")
	if r.str("status") != "QUEUED" || r.Header().Get("Location") != "/v1/products/"+id+"/checks/"+checkID {
		t.Fatalf("trigger: %s", r.Body.String())
	}
	last := h.queue.msgs[len(h.queue.msgs)-1]
	if last.CheckID != checkID || last.Trigger != domain.TriggerManual || last.UserID != "alice" {
		t.Fatalf("enqueued %+v", last)
	}

	*h.clock = t0.Add(time.Minute)
	limited := h.do("POST", "/v1/products/"+id+"/checks", nil)
	expect(t, limited, http.StatusTooManyRequests)
	if limited.Header().Get("Retry-After") != "240" {
		t.Fatalf("Retry-After = %q", limited.Header().Get("Retry-After"))
	}

	got := h.do("GET", "/v1/products/"+id+"/checks/"+checkID, nil)
	expect(t, got, http.StatusOK)
	if got.str("trigger") != "MANUAL" {
		t.Fatalf("check: %s", got.Body.String())
	}
	list := h.do("GET", "/v1/products/"+id+"/checks", nil)
	expect(t, list, http.StatusOK)
	if n := len(list.body["items"].([]any)); n != 2 { // INITIAL + MANUAL
		t.Fatalf("checks listed: %d", n)
	}
	expect(t, h.do("GET", "/v1/products/"+id+"/checks/nope", nil), http.StatusNotFound)
}

func TestMeRetailersAndMisc(t *testing.T) {
	h := newHarness(t, 10)
	me := h.do("GET", "/v1/me", nil)
	expect(t, me, http.StatusOK)
	if me.str("email") != "alice@example.com" || me.body["notifications_enabled"] != true {
		t.Fatalf("me: %s", me.Body.String())
	}
	off := h.do("PATCH", "/v1/me", map[string]any{"notifications_enabled": false})
	expect(t, off, http.StatusOK)
	if off.body["notifications_enabled"] != false {
		t.Fatalf("toggle: %s", off.Body.String())
	}
	expect(t, h.do("PATCH", "/v1/me", map[string]any{}), http.StatusBadRequest)

	rs := h.do("GET", "/v1/retailers", nil)
	expect(t, rs, http.StatusOK)
	if rs.body["generic_allowed"] != true || len(rs.body["items"].([]any)) < 2 {
		t.Fatalf("retailers: %s", rs.Body.String())
	}

	expect(t, h.do("GET", "/healthz", nil), http.StatusOK)
	nf := h.do("GET", "/v1/nope", nil)
	expect(t, nf, http.StatusNotFound)
	if !strings.HasPrefix(nf.Header().Get("Content-Type"), "application/problem+json") || nf.str("request_id") == "" {
		t.Fatalf("404 problem: %v %s", nf.Header(), nf.Body.String())
	}
}
