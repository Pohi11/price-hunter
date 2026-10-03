// Command loadtest measures how the system handles a burst of new tracked
// products: API create latency, and how long the queue + workers take to
// price every one of them. It only ever targets the demo store.
//
//	loadtest -api http://localhost:8088 -dev-user loadtest -store http://localhost:8081 -n 1000
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
	"sort"
	"strings"
	"sync"
	"time"
)

var healthy = []string{"air-fryer", "smartphone-x", "sedan-se", "espresso-machine", "anc-headphones", "robot-vacuum"}

type client struct {
	api, token, devUser string
	hc                  *http.Client
}

func (c *client) do(ctx context.Context, method, path string, body, out any) (int, error) {
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
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp.StatusCode, nil
}

type item struct {
	ID           string     `json:"id"`
	CurrentPrice *any       `json:"current_price"`
	LastError    *any       `json:"last_error"`
	CreatedAt    time.Time  `json:"created_at"`
	LastChecked  *time.Time `json:"last_checked_at"`
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[min(len(d)-1, int(float64(len(d))*p))]
}

func main() {
	api := flag.String("api", "http://localhost:8088", "API base URL")
	token := flag.String("token", os.Getenv("LOADTEST_TOKEN"), "bearer token (AWS)")
	devUser := flag.String("dev-user", "", "X-Dev-User (local)")
	store := flag.String("store", "http://localhost:8081", "demo store base URL (the only allowed target)")
	n := flag.Int("n", 500, "products to create")
	conc := flag.Int("concurrency", 16, "concurrent create requests")
	timeout := flag.Duration("timeout", 15*time.Minute, "overall timeout")
	cleanup := flag.Bool("cleanup", true, "delete created products afterwards")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c := &client{api: *api, token: *token, devUser: *devUser, hc: &http.Client{Timeout: 30 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: *conc * 2}}}

	// 1. Create products concurrently.
	fmt.Printf("creating %d products (concurrency %d)...\n", *n, *conc)
	var (
		mu       sync.Mutex
		created  = map[string]time.Time{}
		latency  []time.Duration
		failures int
		wg       sync.WaitGroup
	)
	jobs := make(chan int)
	start := time.Now()
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				slug := healthy[i%len(healthy)]
				var it item
				t := time.Now()
				_, err := c.do(ctx, http.MethodPost, "/v1/products", map[string]any{
					"name": fmt.Sprintf("Load %05d %s", i, slug),
					// A distinct query parameter makes each URL a distinct product.
					"url":          fmt.Sprintf("%s/p/%s?lt=%d", strings.TrimRight(*store, "/"), slug, i),
					"target_price": map[string]string{"amount": "1.00", "currency": "USD"}, "check_frequency": "24h",
				}, &it)
				mu.Lock()
				if err != nil {
					failures++
					if failures <= 3 {
						fmt.Fprintln(os.Stderr, "create:", err)
					}
				} else {
					created[it.ID] = time.Now()
					latency = append(latency, time.Since(t))
				}
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < *n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	createDur := time.Since(start)
	fmt.Printf("created %d in %s (%.0f/s), %d failed; create latency p50 %s p95 %s\n",
		len(created), createDur.Round(time.Millisecond), float64(len(created))/createDur.Seconds(), failures,
		pct(latency, 0.5).Round(time.Millisecond), pct(latency, 0.95).Round(time.Millisecond))

	// 2. Poll until every product has its first price (or a recorded error).
	firstPrice := map[string]time.Duration{}
	checkErrors := 0
	for len(firstPrice)+checkErrors < len(created) && ctx.Err() == nil {
		cursor := ""
		for {
			var page struct {
				Items      []item `json:"items"`
				NextCursor string `json:"next_cursor"`
			}
			path := "/v1/products?limit=100"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			if _, err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
				fmt.Fprintln(os.Stderr, "list:", err)
				break
			}
			now := time.Now()
			for _, it := range page.Items {
				t0, ok := created[it.ID]
				if !ok {
					continue
				}
				if _, done := firstPrice[it.ID]; done {
					continue
				}
				if it.CurrentPrice != nil {
					firstPrice[it.ID] = now.Sub(t0)
				} else if it.LastError != nil {
					checkErrors++
					firstPrice[it.ID] = -1
				}
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		time.Sleep(time.Second)
	}
	total := time.Since(start)
	var ttfp []time.Duration
	for _, d := range firstPrice {
		if d >= 0 {
			ttfp = append(ttfp, d)
		}
	}
	fmt.Printf("\n## Load test result\n\n")
	fmt.Printf("| products | created | priced | check errors | create p50 | create p95 | first price p50 | first price p95 | first price max | all priced after | throughput |\n")
	fmt.Printf("|---|---|---|---|---|---|---|---|---|---|---|\n")
	fmt.Printf("| %d | %d | %d | %d | %s | %s | %s | %s | %s | %s | %.1f checks/s |\n\n",
		*n, len(created), len(ttfp), checkErrors,
		pct(latency, 0.5).Round(time.Millisecond), pct(latency, 0.95).Round(time.Millisecond),
		pct(ttfp, 0.5).Round(100*time.Millisecond), pct(ttfp, 0.95).Round(100*time.Millisecond), pct(ttfp, 1).Round(100*time.Millisecond),
		total.Round(time.Second), float64(len(ttfp))/total.Seconds())

	// 3. Clean up.
	if *cleanup {
		ids := make(chan string)
		var cw sync.WaitGroup
		for w := 0; w < *conc; w++ {
			cw.Add(1)
			go func() {
				defer cw.Done()
				for id := range ids {
					_, _ = c.do(context.Background(), http.MethodDelete, "/v1/products/"+id, nil, nil)
				}
			}()
		}
		for id := range created {
			ids <- id
		}
		close(ids)
		cw.Wait()
		fmt.Printf("deleted %d products\n", len(created))
	}
}
