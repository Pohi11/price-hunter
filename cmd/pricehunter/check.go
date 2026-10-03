package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Pohi11/price-hunter/internal/fetch"
	"github.com/Pohi11/price-hunter/internal/retailer"
	"github.com/Pohi11/price-hunter/internal/urlx"
)

// runCheck implements "pricehunter check <url>": validate, fetch safely,
// extract, print. It exercises the same code path the worker uses.
func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	allowLocal := fs.Bool("allow-localhost", false, "allow http://localhost targets (demo store); dev only")
	generic := fs.Bool("generic", true, "allow retailers without a profile (structured data only)")
	timeout := fs.Duration("timeout", 20*time.Second, "overall timeout")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "Usage: pricehunter check [flags] <url>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	u, err := urlx.Validate(fs.Arg(0), urlx.Options{AllowLocalhost: *allowLocal})
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	reg, err := retailer.Default(retailer.Options{AllowGeneric: *generic})
	if err != nil {
		return err
	}
	opts := fetch.DefaultOptions()
	opts.Guard = fetch.Guard{AllowLoopback: *allowLocal}
	opts.HostRate = reg.RateFor
	src := retailer.NewSource(fetch.New(opts), reg)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	start := time.Now()
	q, qerr := src.Quote(ctx, urlx.StripTracking(u))
	outcome, retryAfter := retailer.Classify(qerr)

	if *asJSON {
		out := map[string]any{"url": u.String(), "retailer": q.Retailer, "outcome": outcome, "elapsed_ms": time.Since(start).Milliseconds()}
		if qerr == nil {
			out["price"] = q.Offer.Price.Decimal()
			out["currency"] = q.Offer.Price.Currency
			out["availability"] = q.Offer.Availability
			out["strategy"] = q.Offer.Strategy
			out["title"] = q.Offer.Title
			out["strategies_disagree"] = q.Disagreement
		} else {
			out["error"] = qerr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else if qerr == nil {
		title := q.Offer.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("%s — %s (%s, %s via %s) in %s\n", title, q.Offer.Price, q.Offer.Availability,
			q.Retailer, q.Offer.Strategy, time.Since(start).Round(time.Millisecond))
		if q.Disagreement {
			fmt.Println("warning: extraction strategies disagree on the price")
		}
	}

	if qerr != nil {
		if errors.Is(qerr, context.DeadlineExceeded) {
			return fmt.Errorf("%s: timed out", outcome)
		}
		msg := fmt.Sprintf("%s: %v", outcome, qerr)
		if retryAfter > 0 {
			msg += fmt.Sprintf(" (retry after %s)", retryAfter)
		}
		return errors.New(msg)
	}
	return nil
}
