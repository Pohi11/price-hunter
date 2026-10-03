package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Pohi11/price-hunter/internal/app"
	"github.com/Pohi11/price-hunter/internal/config"
	"github.com/Pohi11/price-hunter/internal/demostore"
	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/products"
	"github.com/Pohi11/price-hunter/internal/queue"
	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// seedItem is one demo watchlist entry. Targets are chosen so the demo
// shows a mix of states: one alert, near misses, and a failure.
type seedItem struct {
	slug, name, target, freq string
}

var seedWatchlist = []seedItem{
	{"air-fryer", "Turbo Air Fryer 5.5qt", "130.00", "6h"},
	{"smartphone-x", "Smartphone X 128GB", "850.00", "12h"},
	{"sedan-se", "2026 Midsize Sedan SE", "24500.00", "24h"},
	{"espresso-machine", "Barista Pro Espresso Machine", "480.00", "6h"},
	{"anc-headphones", "Quiet Comfort ANC Headphones", "240.00", "6h"},
	{"robot-vacuum", "AutoClean Robot Vacuum", "260.00", "12h"},
	{"glitch-tv", "Glitchy 55\" TV", "450.00", "12h"},
	{"broken-toaster", "Broken Toaster", "35.00", "24h"},
}

// runSeed creates a demo watchlist with backfilled price history computed
// from the demo store's deterministic price curves. Local only: it writes
// to the table directly.
func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	base := fs.String("store", "http://localhost:8081", "demo store base URL")
	days := fs.Int("days", 60, "days of history to backfill")
	user := fs.String("user", "", "user ID to seed (default PH_DEV_USER)")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.IsLocal() {
		return errors.New("seed writes directly to the table and only runs with PH_ENV=local")
	}
	if *user == "" {
		*user = cfg.DevUser
	}
	log := telemetry.NewLogger(os.Stderr, "warn")
	ctx := context.Background()
	a, err := app.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	for _, it := range seedWatchlist {
		dp, ok := demostore.Find(it.slug)
		if !ok {
			return fmt.Errorf("unknown demo product %s", it.slug)
		}
		p, _, err := a.Products.Create(ctx, *user, cfg.DevEmail, products.CreateInput{
			Name: it.name, URL: strings.TrimRight(*base, "/") + "/p/" + it.slug,
			Target: products.MoneyInput{Amount: it.target, Currency: "USD"}, Frequency: it.freq,
		})
		if err != nil {
			var verr *domain.ValidationError
			if errors.As(err, &verr) || errors.Is(err, domain.ErrDuplicate) {
				fmt.Printf("skip %-32s %v\n", it.name, err)
				continue
			}
			return err
		}
		if dp.Fault == demostore.FaultNoPrice {
			fmt.Printf("seeded %-30s (no history: page has no price)\n", it.name)
			continue
		}

		// Backfill one point per check interval, as the worker would have.
		step := p.Frequency.Duration()
		var pts []domain.PricePoint
		var lowest, trusted *domain.Money
		for t := now.Add(-time.Duration(*days) * 24 * time.Hour).Truncate(step); t.Before(now); t = t.Add(step) {
			price := dp.PriceAt(t)
			pt := domain.PricePoint{ProductID: p.ID, ObservedAt: t, Price: price, Availability: dp.AvailabilityAt(t), Strategy: string(dp.Layout)}
			if trusted != nil && isAnomaly(*trusted, price) {
				pt.Suspect = true // the glitch TV's "99% off" bug, as the anomaly gate would have recorded it
			} else {
				t := price
				trusted = &t
				if lowest == nil || price.Minor < lowest.Minor {
					l := price
					lowest = &l
				}
			}
			pts = append(pts, pt)
		}
		if err := a.Store.PutPricePoints(ctx, pts); err != nil {
			return err
		}
		last := pts[len(pts)-1]
		for i := len(pts) - 1; i >= 0 && last.Suspect; i-- {
			last = pts[i] // current price is the latest trusted one
		}
		cur := last.Price
		p.Current, p.Lowest, p.Availability = &cur, lowest, last.Availability
		p.LastCheckedAt, p.LastSuccessAt = &last.ObservedAt, &last.ObservedAt
		if s, fire := domain.EvaluateAlert(domain.AlertArmed, p.Target, cur, last.Availability); fire {
			p.AlertState, p.LastAlertAt = s, &last.ObservedAt
		}
		next := now.Add(time.Minute) // let the running server take over shortly
		p.NextCheckAt = &next
		if err := a.Store.SetTrackingState(ctx, p); err != nil {
			return err
		}
		fmt.Printf("seeded %-30s %4d points, now %s, low %s, target %s\n", it.name, len(pts), cur.Decimal(), lowest.Decimal(), p.Target.Decimal())
	}

	// Run the first checks that Create queued, against the live demo store,
	// so every product ends with a real, current observation.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	go func() {
		for runCtx.Err() == nil {
			if d, _ := a.MemQueue.Depth(runCtx); d == 0 {
				time.Sleep(2 * time.Second) // let in-flight checks finish
				cancel()
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	a.MemQueue.Consume(runCtx, queue.ConsumeOptions{Workers: 4, Drain: 5 * time.Second}, a.Checker.Run)
	fmt.Println("ran first checks against", *base)
	return nil
}

func isAnomaly(prev, cur domain.Money) bool {
	r := float64(cur.Minor) / float64(prev.Minor)
	return r < 0.3 || r > 3
}
