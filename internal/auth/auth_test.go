package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDevResolverAndRequire(t *testing.T) {
	var seen Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
	})
	unauthorized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	h := Require(Dev("local-user", "me@example.com"), unauthorized, next)

	cases := []struct {
		header   string
		wantUser string
		wantCode int
	}{
		{"", "local-user", 200},
		{"bob", "bob", 200},
		{"bad user!", "", 401},
		{"../../etc", "", 401},
	}
	for _, c := range cases {
		seen = Identity{}
		req := httptest.NewRequest("GET", "/v1/products", nil)
		if c.header != "" {
			req.Header.Set("X-Dev-User", c.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.wantCode || seen.UserID != c.wantUser {
			t.Errorf("header %q: code %d user %q, want %d %q", c.header, rec.Code, seen.UserID, c.wantCode, c.wantUser)
		}
	}
}
