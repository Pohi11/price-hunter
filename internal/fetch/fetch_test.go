package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuardCheckAddr(t *testing.T) {
	g := Guard{}
	tests := []struct {
		ip      string
		port    int
		allowed bool
	}{
		{"93.184.215.14", 443, true},
		{"93.184.215.14", 80, true},
		{"2606:2800:21f:cb07:6820:80da:af6b:8b2c", 443, true},
		{"93.184.215.14", 8080, false},
		{"93.184.215.14", 22, false},
		{"127.0.0.1", 80, false},
		{"127.10.20.30", 443, false},
		{"10.0.0.5", 443, false},
		{"172.16.0.1", 443, false},
		{"192.168.1.1", 80, false},
		{"169.254.169.254", 80, false}, // EC2 instance metadata
		{"169.254.170.2", 80, false},   // ECS task metadata
		{"100.64.0.1", 80, false},      // CGNAT
		{"0.0.0.0", 80, false},
		{"255.255.255.255", 80, false},
		{"224.0.0.1", 80, false},
		{"198.18.0.1", 80, false},
		{"::1", 443, false},
		{"::", 443, false},
		{"::ffff:127.0.0.1", 443, false},      // IPv4-mapped loopback
		{"::ffff:169.254.169.254", 80, false}, // IPv4-mapped metadata
		{"fe80::1", 443, false},
		{"fc00::1", 443, false},
		{"fd00:ec2::254", 80, false},       // EC2 IPv6 metadata
		{"64:ff9b::a9fe:a9fe", 80, false},  // NAT64 of 169.254.169.254
		{"2002:a9fe:a9fe::1", 80, false},   // 6to4 of 169.254.169.254
		{"2001:0:4136:e378::1", 80, false}, // Teredo
		{"ff02::1", 80, false},
	}
	for _, tt := range tests {
		err := g.CheckAddr(netip.MustParseAddr(tt.ip), tt.port)
		if tt.allowed && err != nil {
			t.Errorf("%s:%d rejected: %v", tt.ip, tt.port, err)
		}
		if !tt.allowed && !errors.Is(err, ErrForbiddenTarget) {
			t.Errorf("%s:%d allowed, want forbidden", tt.ip, tt.port)
		}
	}
}

func TestGuardLoopbackOptIn(t *testing.T) {
	g := Guard{AllowLoopback: true}
	if err := g.CheckAddr(netip.MustParseAddr("127.0.0.1"), 8081); err != nil {
		t.Fatalf("loopback rejected with opt-in: %v", err)
	}
	if err := g.CheckAddr(netip.MustParseAddr("169.254.169.254"), 80); err == nil {
		t.Fatal("opt-in must only cover loopback")
	}
}

// newTestClient returns a client that may reach httptest servers on loopback
// and records sleeps instead of sleeping.
func newTestClient(t *testing.T, mod func(*Options)) (*Client, *[]time.Duration) {
	t.Helper()
	var slept []time.Duration
	opts := Options{
		Guard:         Guard{AllowLoopback: true},
		Timeout:       2 * time.Second,
		RatePerSecond: 1000,
		Burst:         100,
		sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	if mod != nil {
		mod(&opts)
	}
	return New(opts), &slept
}

func html(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func wantKind(t *testing.T, err error, kind Kind) Error {
	t.Helper()
	fe, ok := AsError(err)
	if !ok {
		t.Fatalf("error %v (%T) is not *fetch.Error", err, err)
	}
	if fe.Kind != kind {
		t.Fatalf("kind = %s, want %s (err: %v)", fe.Kind, kind, err)
	}
	return *fe
}

func TestGetSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "PriceHunter/") {
			t.Errorf("User-Agent = %q", ua)
		}
		html(w, "<html>ok</html>")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, nil)
	resp, err := c.Get(context.Background(), srv.URL+"/p/1")
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "<html>ok</html>" || resp.Attempts != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestGetStatusClassification(t *testing.T) {
	tests := []struct {
		status int
		kind   Kind
	}{
		{404, KindNotFound}, {410, KindNotFound}, {403, KindBlocked}, {401, KindBlocked},
		{429, KindRateLimited}, {500, KindTransient}, {503, KindTransient}, {418, KindHTTP},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
		}))
		c, _ := newTestClient(t, func(o *Options) { o.MaxRetries = -1 }) // no retries
		_, err := c.Get(context.Background(), srv.URL)
		fe := wantKind(t, err, tt.kind)
		if fe.Status != tt.status {
			t.Errorf("status %d recorded as %d", tt.status, fe.Status)
		}
		srv.Close()
	}
}

func TestGetRetriesTransientThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		html(w, "ok")
	}))
	defer srv.Close()
	c, slept := newTestClient(t, nil)
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempts != 3 || len(*slept) != 2 {
		t.Fatalf("attempts=%d sleeps=%v", resp.Attempts, *slept)
	}
}

func TestGetGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, nil)
	_, err := c.Get(context.Background(), srv.URL)
	wantKind(t, err, KindTransient)
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (1 + 2 retries)", calls.Load())
	}
}

func TestGetDoesNotRetryPermanentErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, nil)
	_, _ = c.Get(context.Background(), srv.URL)
	if calls.Load() != 1 {
		t.Fatalf("404 retried: %d calls", calls.Load())
	}
}

func TestGetRetryAfter(t *testing.T) {
	t.Run("short retry-after is waited in-request", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			html(w, "ok")
		}))
		defer srv.Close()
		c, slept := newTestClient(t, nil)
		if _, err := c.Get(context.Background(), srv.URL); err != nil {
			t.Fatal(err)
		}
		if len(*slept) != 1 || (*slept)[0] != time.Second {
			t.Fatalf("sleeps = %v, want [1s]", *slept)
		}
	})
	t.Run("long retry-after is returned to the caller", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()
		c, slept := newTestClient(t, nil)
		_, err := c.Get(context.Background(), srv.URL)
		fe := wantKind(t, err, KindRateLimited)
		if fe.RetryAfter != time.Hour || len(*slept) != 0 {
			t.Fatalf("RetryAfter=%v sleeps=%v", fe.RetryAfter, *slept)
		}
	})
}

func TestGetBodyLimits(t *testing.T) {
	t.Run("oversized body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			html(w, strings.Repeat("x", 2048))
		}))
		defer srv.Close()
		c, _ := newTestClient(t, func(o *Options) { o.MaxBody = 1024 })
		_, err := c.Get(context.Background(), srv.URL)
		wantKind(t, err, KindTooLarge)
	})
	t.Run("gzip bomb is capped after decompression", func(t *testing.T) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(bytes.Repeat([]byte{'a'}, 10<<20)) // 10 MB -> ~10 KB compressed
		_ = zw.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(buf.Bytes())
		}))
		defer srv.Close()
		c, _ := newTestClient(t, func(o *Options) { o.MaxBody = 1 << 20 })
		_, err := c.Get(context.Background(), srv.URL)
		wantKind(t, err, KindTooLarge)
	})
}

func TestGetRejectsUnsupportedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	}))
	defer srv.Close()
	c, _ := newTestClient(t, nil)
	_, err := c.Get(context.Background(), srv.URL)
	wantKind(t, err, KindUnsupported)
}

func TestGetTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c, _ := newTestClient(t, func(o *Options) { o.Timeout = 150 * time.Millisecond; o.MaxRetries = -1 })
	start := time.Now()
	_, err := c.Get(context.Background(), srv.URL)
	wantKind(t, err, KindTransient)
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not enforced")
	}
}

func TestSSRFDialGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached a loopback server")
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]

	c, _ := newTestClient(t, func(o *Options) { o.Guard = Guard{} }) // production guard
	for _, target := range []string{
		srv.URL,                   // 127.0.0.1 literal
		"http://localhost" + port, // hostname that *resolves* to loopback (rebinding-style)
	} {
		_, err := c.Get(context.Background(), target)
		wantKind(t, err, KindForbidden)
	}
}

func TestSSRFRedirectToMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/iam/security-credentials/", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, nil)
	_, err := c.Get(context.Background(), srv.URL)
	wantKind(t, err, KindForbidden)
}

func TestRedirectLimit(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, nil)
	_, err := c.Get(context.Background(), srv.URL+"/")
	wantKind(t, err, KindHTTP)
}

func TestRobots(t *testing.T) {
	var robotsCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			robotsCalls.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\n"))
			return
		}
		html(w, "ok")
	}))
	defer srv.Close()
	c, _ := newTestClient(t, func(o *Options) { o.RespectRobots = true })

	if _, err := c.Get(context.Background(), srv.URL+"/p/1"); err != nil {
		t.Fatalf("allowed path: %v", err)
	}
	_, err := c.Get(context.Background(), srv.URL+"/private/item")
	wantKind(t, err, KindDisallowed)
	if robotsCalls.Load() != 1 {
		t.Fatalf("robots.txt fetched %d times, want 1 (cached)", robotsCalls.Load())
	}
}

func TestRobotsMissingAndBroken(t *testing.T) {
	t.Run("404 robots means allow all", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				http.NotFound(w, r)
				return
			}
			html(w, "ok")
		}))
		defer srv.Close()
		c, _ := newTestClient(t, func(o *Options) { o.RespectRobots = true })
		if _, err := c.Get(context.Background(), srv.URL+"/anything"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("5xx robots is transient, not cached", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		c, _ := newTestClient(t, func(o *Options) { o.RespectRobots = true })
		_, err := c.Get(context.Background(), srv.URL+"/p")
		wantKind(t, err, KindTransient)
	})
}

func TestHostLimiterIsShared(t *testing.T) {
	h := newHostLimiters(1, 1, func(host string) float64 {
		if host == "fast.example" {
			return 50
		}
		return 0
	})
	first, second := h.get("a.example"), h.get("a.example")
	if first != second {
		t.Fatal("same host returned different limiters")
	}
	if h.get("a.example") == h.get("b.example") {
		t.Fatal("different hosts share a limiter")
	}
	if h.get("fast.example").Limit() != 50 || h.get("a.example").Limit() != 1 {
		t.Fatal("per-host rate override not applied")
	}
}

func TestRateLimitIsApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { html(w, "ok") }))
	defer srv.Close()
	c, _ := newTestClient(t, func(o *Options) { o.RatePerSecond = 10; o.Burst = 1 })
	start := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := c.Get(context.Background(), srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	// 4 requests at 10/s with burst 1 need at least ~300ms.
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Fatalf("4 requests took %v; limiter not applied", el)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("120", now); d != 2*time.Minute {
		t.Fatalf("seconds form: %v", d)
	}
	if d := parseRetryAfter(now.Add(time.Hour).Format(http.TimeFormat), now); d != time.Hour {
		t.Fatalf("date form: %v", d)
	}
	if d := parseRetryAfter("garbage", now); d != 0 {
		t.Fatalf("garbage: %v", d)
	}
}
