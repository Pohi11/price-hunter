// Package api is the REST API: routing, request decoding and validation of
// wire formats, and the mapping of domain errors to problem+json. Business
// rules live in package products.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Pohi11/price-hunter/internal/auth"
	"github.com/Pohi11/price-hunter/internal/products"
	"github.com/Pohi11/price-hunter/internal/retailer"
)

// Options configures the API.
type Options struct {
	Auth        auth.Resolver
	CORSOrigins []string // exact origins allowed for browser calls (local dev); API Gateway handles CORS in AWS
	MaxProducts int      // reported to clients in /v1/me
	Version     string
	Now         func() time.Time
}

// API holds handler dependencies.
type API struct {
	svc      *products.Service
	registry *retailer.Registry
	opts     Options
	log      *slog.Logger
}

// New returns the API's http.Handler.
func New(svc *products.Service, reg *retailer.Registry, log *slog.Logger, opts Options) http.Handler {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	a := &API{svc: svc, registry: reg, opts: opts, log: log}

	v1 := http.NewServeMux()
	v1.HandleFunc("POST /v1/products", a.createProduct)
	v1.HandleFunc("GET /v1/products", a.listProducts)
	v1.HandleFunc("GET /v1/products/{id}", a.getProduct)
	v1.HandleFunc("PATCH /v1/products/{id}", a.updateProduct)
	v1.HandleFunc("DELETE /v1/products/{id}", a.deleteProduct)
	v1.HandleFunc("GET /v1/products/{id}/prices", a.getPrices)
	v1.HandleFunc("POST /v1/products/{id}/checks", a.triggerCheck)
	v1.HandleFunc("GET /v1/products/{id}/checks", a.listChecks)
	v1.HandleFunc("GET /v1/products/{id}/checks/{checkId}", a.getCheck)
	v1.HandleFunc("GET /v1/me", a.getMe)
	v1.HandleFunc("PATCH /v1/me", a.updateMe)
	v1.HandleFunc("GET /v1/retailers", a.listRetailers)

	unauthorized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, problem{Type: problemType("unauthorized"), Title: "Authentication required", Status: http.StatusUnauthorized})
	})
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, problem{Type: problemType("not-found"), Title: "Not found", Status: http.StatusNotFound})
	})

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": opts.Version})
	})
	root.Handle("/v1/", auth.Require(opts.Auth, unauthorized, withNotFound(v1, notFound)))

	var h http.Handler = root
	h = cors(opts.CORSOrigins, h)
	h = a.accessLog(h)
	h = recoverer(a.log, h)
	h = requestID(h)
	return h
}

// withNotFound returns problem+json for unmatched routes instead of the
// mux's plain-text 404 / 405.
func withNotFound(mux *http.ServeMux, notFound http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == "" {
			notFound.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
