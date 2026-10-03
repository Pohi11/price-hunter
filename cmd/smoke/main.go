// Command smoke verifies a deployed environment end to end through its
// public API: create a product on the demo store -> the scheduler/worker
// price it -> the alert triggers -> a manual check runs -> cleanup.
//
//	smoke -api https://xyz.execute-api.us-east-1.amazonaws.com -token "$ID_TOKEN" \
//	      -product-url https://abc.lambda-url.us-east-1.on.aws/p/air-fryer
//	smoke -api http://localhost:8088 -dev-user smoke -product-url http://localhost:8081/p/air-fryer
//
// Exit status is non-zero on any failed step, so CI can gate promotion.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type client struct {
	api, token, devUser string
	hc                  *http.Client
}

func (c *client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.api, "/")+path, rdr)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.devUser != "" {
		req.Header.Set("X-Dev-User", c.devUser)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && len(raw) > 0 && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	if resp.StatusCode >= 300 && out != nil {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp.StatusCode, nil
}

type product struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	CurrentPrice *struct {
		Amount, Currency string
	} `json:"current_price"`
	Alert struct {
		State string `json:"state"`
	} `json:"alert"`
	LastError *struct {
		Code string `json:"code"`
	} `json:"last_error"`
}

type check struct {
	CheckID string `json:"check_id"`
	Status  string `json:"status"`
}

func main() {
	api := flag.String("api", os.Getenv("SMOKE_API"), "API base URL")
	token := flag.String("token", os.Getenv("SMOKE_TOKEN"), "Cognito ID token (AWS)")
	devUser := flag.String("dev-user", "", "X-Dev-User for local server mode")
	productURL := flag.String("product-url", os.Getenv("SMOKE_PRODUCT_URL"), "demo store product page to track")
	timeout := flag.Duration("timeout", 4*time.Minute, "overall timeout")
	readOnly := flag.Bool("read-only", false, "only check health and that auth is enforced (prod)")
	flag.Parse()
	if *api == "" || (*productURL == "" && !*readOnly) {
		fail("usage: smoke -api URL -product-url URL [-token T | -dev-user U]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c := &client{api: *api, token: *token, devUser: *devUser, hc: &http.Client{Timeout: 20 * time.Second}}
	start := time.Now()
	ok := true
	// step runs fn unless an earlier step failed. Failures are reported, and
	// cleanup below still runs, so a failed smoke test leaves no product behind.
	step := func(name string, fn func() error) {
		if !ok {
			return
		}
		t := time.Now()
		if err := fn(); err != nil {
			ok = false
			fmt.Fprintf(os.Stderr, "  FAIL %-48s %v\n", name, err)
			return
		}
		fmt.Printf("  ok   %-48s %s\n", name, time.Since(t).Round(time.Millisecond))
	}

	step("health", func() error {
		var h struct{ Status, Version string }
		if _, err := c.do(ctx, http.MethodGet, "/healthz", nil, &h); err != nil {
			return err
		}
		if h.Status != "ok" {
			return fmt.Errorf("status %q", h.Status)
		}
		fmt.Printf("    version %s\n", h.Version)
		return nil
	})

	if *readOnly {
		step("unauthenticated request rejected", func() error {
			code, _ := (&client{api: c.api, hc: c.hc}).do(ctx, http.MethodGet, "/v1/products", nil, nil)
			if code != http.StatusUnauthorized {
				return fmt.Errorf("anonymous request returned %d", code)
			}
			return nil
		})
		if !ok {
			fail("smoke FAILED")
		}
		fmt.Printf("smoke (read-only) OK in %s\n", time.Since(start).Round(time.Second))
		return
	}

	var p product
	step("create product", func() error {
		_, err := c.do(ctx, http.MethodPost, "/v1/products", map[string]any{
			"name": fmt.Sprintf("Smoke test %s", time.Now().UTC().Format("150405")), "url": *productURL,
			"target_price": map[string]string{"amount": "100000.00", "currency": "USD"}, "check_frequency": "24h",
		}, &p)
		return err
	})

	step("first check prices it (scheduler/worker path)", func() error {
		for {
			if _, err := c.do(ctx, http.MethodGet, "/v1/products/"+p.ID, nil, &p); err != nil {
				return err
			}
			if p.CurrentPrice != nil {
				fmt.Printf("    %s %s\n", p.CurrentPrice.Amount, p.CurrentPrice.Currency)
				return nil
			}
			if p.LastError != nil {
				return fmt.Errorf("check failed: %s", p.LastError.Code)
			}
			if err := sleep(ctx, 3*time.Second); err != nil {
				return fmt.Errorf("no price before timeout")
			}
		}
	})

	step("alert triggered (target above price)", func() error {
		if p.Alert.State != "TRIGGERED" {
			return fmt.Errorf("alert state %q", p.Alert.State)
		}
		return nil
	})

	var chk check
	step("manual check succeeds", func() error {
		if _, err := c.do(ctx, http.MethodPost, "/v1/products/"+p.ID+"/checks", nil, &chk); err != nil {
			return err
		}
		for chk.Status == "QUEUED" || chk.Status == "RUNNING" {
			if err := sleep(ctx, 2*time.Second); err != nil {
				return fmt.Errorf("check still %s at timeout", chk.Status)
			}
			if _, err := c.do(ctx, http.MethodGet, "/v1/products/"+p.ID+"/checks/"+chk.CheckID, nil, &chk); err != nil {
				return err
			}
		}
		if chk.Status != "SUCCEEDED" {
			return fmt.Errorf("check %s", chk.Status)
		}
		return nil
	})

	step("cooldown enforced", func() error {
		code, _ := c.do(ctx, http.MethodPost, "/v1/products/"+p.ID+"/checks", nil, nil)
		if code != http.StatusTooManyRequests {
			return fmt.Errorf("second manual check returned %d, want 429", code)
		}
		return nil
	})

	step("price history recorded", func() error {
		var h struct {
			Points []any `json:"points"`
		}
		if _, err := c.do(ctx, http.MethodGet, "/v1/products/"+p.ID+"/prices?max_points=100", nil, &h); err != nil {
			return err
		}
		if len(h.Points) < 2 {
			return fmt.Errorf("%d points, want >= 2", len(h.Points))
		}
		return nil
	})

	step("unauthenticated request rejected", func() error {
		anon := &client{api: c.api, hc: c.hc}
		code, _ := anon.do(ctx, http.MethodGet, "/v1/products", nil, nil)
		if c.devUser != "" {
			return nil // local dev mode authenticates everyone
		}
		if code != http.StatusUnauthorized {
			return fmt.Errorf("anonymous request returned %d", code)
		}
		return nil
	})

	if p.ID != "" {
		code, err := c.do(context.Background(), http.MethodDelete, "/v1/products/"+p.ID, nil, nil)
		fmt.Printf("  cleanup: DELETE -> %d %s\n", code, errOrEmpty(err))
	}
	if !ok {
		fail("smoke FAILED")
	}
	fmt.Printf("smoke OK in %s\n", time.Since(start).Round(time.Second))
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func errOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
