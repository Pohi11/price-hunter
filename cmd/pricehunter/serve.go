package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Pohi11/price-hunter/internal/app"
	"github.com/Pohi11/price-hunter/internal/auth"
	"github.com/Pohi11/price-hunter/internal/config"
	"github.com/Pohi11/price-hunter/internal/queue"
	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// runServe runs server mode: HTTP API + scheduler ticker + worker pool in
// one process, with graceful shutdown. This is how the app runs locally,
// and how it would run in a container (ECS) without any code changes.
func runServe(_ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := telemetry.NewLogger(os.Stdout, cfg.LogLevel, "service", "pricehunter", "env", string(cfg.Env), "version", cfg.Version)
	if !cfg.IsLocal() {
		return errors.New("serve: server mode authenticates with the dev resolver and is only for PH_ENV=local")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.Build(ctx, cfg, log)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	apiHandler := a.APIHandler(auth.Dev(cfg.DevUser, cfg.DevEmail))
	mux.Handle("/v1/", apiHandler)
	mux.Handle("/healthz", apiHandler)
	if cfg.WebDir != "" {
		spa, err := spaHandler(cfg.WebDir)
		if err != nil {
			return err
		}
		mux.Handle("/", spa)
	}
	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("http server listening", "addr", cfg.HTTPAddr, "web_dir", cfg.WebDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx) // stops accepting, finishes in-flight requests
	})

	// Scheduler: one pass immediately, then every interval.
	g.Go(func() error {
		t := time.NewTicker(cfg.SchedulerInterval)
		defer t.Stop()
		for {
			if _, err := a.Scheduler.Run(gctx); err != nil {
				log.Error("scheduler run failed", "err", err)
			}
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
			}
		}
	})

	// Workers: in-process queue by default, SQS (e.g. ElasticMQ) when
	// PH_CHECK_QUEUE_URL is set.
	pool := queue.ConsumeOptions{Workers: cfg.WorkerConcurrency, Drain: 20 * time.Second, Log: log}
	g.Go(func() error {
		log.Info("workers started", "concurrency", cfg.WorkerConcurrency, "sqs", a.SQSQueue != nil)
		if a.SQSQueue != nil {
			a.SQSQueue.Consume(gctx, queue.SQSConsumeOptions{ConsumeOptions: pool}, a.Checker.Run)
		} else {
			a.MemQueue.Consume(gctx, pool, a.Checker.Run)
		}
		log.Info("workers drained")
		return nil
	})

	err = g.Wait()
	log.Info("shutdown complete")
	return err
}

// spaHandler serves the built web app, falling back to index.html for
// client-side routes. os.Root confines every file access to dir, so path
// traversal is impossible by construction.
func spaHandler(dir string) (http.Handler, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("web dir: %w", err)
	}
	files := http.FileServerFS(root.FS())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if info, err := root.Stat(name); name == "" || err != nil || info.IsDir() {
			if !strings.Contains(path.Base(r.URL.Path), ".") {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				files.ServeHTTP(w, r2) // index.html
				return
			}
		}
		files.ServeHTTP(w, r)
	}), nil
}
