// Command demostore runs the fictional retailer used for demos and tests.
//
//	demostore -addr :8081 -admin-token secret
//	demostore -dump testdata/pages/demostore -at 2026-10-01T12:00:00Z
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Pohi11/price-hunter/internal/demostore"
)

func main() {
	addr := flag.String("addr", envOr("DEMOSTORE_ADDR", ":8081"), "listen address")
	token := flag.String("admin-token", os.Getenv("DEMOSTORE_ADMIN_TOKEN"), "enable /admin with this bearer token")
	dump := flag.String("dump", "", "write every product page to this directory and exit")
	at := flag.String("at", "", "with -dump: render as of this RFC3339 time")
	flag.Parse()

	if *dump != "" {
		if err := dumpPages(*dump, *at); err != nil {
			fmt.Fprintln(os.Stderr, "dump:", err)
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	srv := &http.Server{
		Addr:              *addr,
		Handler:           demostore.New(demostore.Options{AdminToken: *token}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("demo store listening", "addr", *addr, "admin", *token != "")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// dumpPages renders each non-faulty product page at a fixed time, for use
// as golden extraction fixtures.
func dumpPages(dir, at string) error {
	now := time.Now().UTC()
	if at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return err
		}
		now = t
	}
	h := demostore.New(demostore.Options{Now: func() time.Time { return now }})
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // fixtures are public test data
		return err
	}
	for _, p := range demostore.Catalog() {
		switch p.Fault {
		case demostore.FaultNone, demostore.FaultNoPrice, demostore.FaultRedesign:
		default:
			continue // HTTP-level faults are not page fixtures
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:8081/p/"+p.Slug, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("%s: status %d", p.Slug, rec.Code)
		}
		if err := os.WriteFile(filepath.Join(dir, p.Slug+".html"), rec.Body.Bytes(), 0o644); err != nil { //nolint:gosec // fixtures are public test data
			return err
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
