// Package fetch is Price Hunter's outbound HTTP client. It is the only code
// that talks to retailer sites, and it is built to be safe with
// user-supplied URLs:
//
//   - SSRF protection at dial time (see Guard) plus static checks on every redirect
//   - strict timeouts and a response size cap (applied after decompression)
//   - bounded retries with exponential backoff and full jitter
//   - a per-host token-bucket rate limiter
//   - robots.txt support
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Pohi11/price-hunter/internal/urlx"
)

// Options configures a Client. Zero fields take the defaults from DefaultOptions.
type Options struct {
	Guard         Guard
	UserAgent     string
	Timeout       time.Duration // per attempt, connect through body read
	MaxBody       int64         // bytes, after decompression
	MaxRedirects  int
	MaxRetries    int // in-request retries for transient failures; 0 = default, <0 = none
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration // also the longest Retry-After we will wait in-request
	RatePerSecond float64       // per host, unless HostRate overrides it
	Burst         int
	// HostRate optionally returns a host-specific rate (requests/second);
	// return 0 to use RatePerSecond. Retailer profiles use this.
	HostRate      func(host string) float64
	RespectRobots bool

	// sleep is replaceable in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// DefaultOptions returns production settings.
func DefaultOptions() Options {
	return Options{
		UserAgent:     "PriceHunter/1.0 (+https://github.com/Pohi11/price-hunter)",
		Timeout:       15 * time.Second,
		MaxBody:       3 << 20,
		MaxRedirects:  5,
		MaxRetries:    2,
		BaseBackoff:   300 * time.Millisecond,
		MaxBackoff:    3 * time.Second,
		RatePerSecond: 0.5,
		Burst:         1,
		RespectRobots: true,
	}
}

// Response is a successfully fetched document.
type Response struct {
	URL         string // final URL after redirects
	Status      int
	ContentType string
	Body        []byte
	Duration    time.Duration // total, including retries and rate-limit waits
	Attempts    int
}

// Client fetches retailer pages. It is safe for concurrent use; create one
// per process so connections and rate limiters are shared.
type Client struct {
	hc       *http.Client
	opts     Options
	limiters *hostLimiters
	robots   *robotsCache
}

// New builds a Client.
func New(opts Options) *Client {
	d := DefaultOptions()
	if opts.UserAgent == "" {
		opts.UserAgent = d.UserAgent
	}
	if opts.Timeout == 0 {
		opts.Timeout = d.Timeout
	}
	if opts.MaxBody == 0 {
		opts.MaxBody = d.MaxBody
	}
	if opts.MaxRedirects == 0 {
		opts.MaxRedirects = d.MaxRedirects
	}
	switch {
	case opts.MaxRetries == 0:
		opts.MaxRetries = d.MaxRetries
	case opts.MaxRetries < 0:
		opts.MaxRetries = 0 // explicitly disabled
	}
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = d.BaseBackoff
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = d.MaxBackoff
	}
	if opts.RatePerSecond == 0 {
		opts.RatePerSecond = d.RatePerSecond
	}
	if opts.Burst == 0 {
		opts.Burst = d.Burst
	}
	if opts.sleep == nil {
		opts.sleep = sleepCtx
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second, Control: opts.Guard.Control}
	transport := &http.Transport{
		Proxy:                 nil, // never honor HTTP(S)_PROXY: it would bypass the dial guard
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	c := &Client{opts: opts, limiters: newHostLimiters(opts.RatePerSecond, opts.Burst, opts.HostRate)}
	c.hc = &http.Client{Transport: transport, CheckRedirect: c.checkRedirect}
	c.robots = newRobotsCache(c, 24*time.Hour)
	return c
}

var errTooManyRedirects = errors.New("too many redirects")

// checkRedirect re-validates every hop. The dial guard would also stop a
// redirect to a private IP, but rejecting early gives a clearer error and
// blocks schemes and ports the guard never sees.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= c.opts.MaxRedirects {
		return errTooManyRedirects
	}
	if _, err := urlx.Validate(req.URL.String(), urlx.Options{AllowLocalhost: c.opts.Guard.AllowLoopback}); err != nil {
		return fmt.Errorf("%w: redirect to %s: %w", ErrForbiddenTarget, req.URL.Redacted(), err)
	}
	return nil
}

// Get fetches rawURL as an HTML/JSON document.
func (c *Client) Get(ctx context.Context, rawURL string) (*Response, error) {
	start := time.Now()
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, &Error{Kind: KindForbidden, URL: rawURL, Err: fmt.Errorf("invalid URL")}
	}

	if c.opts.RespectRobots {
		allowed, err := c.robots.allowed(ctx, u)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, &Error{Kind: KindDisallowed, URL: rawURL, Err: errors.New("disallowed by robots.txt")}
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.opts.MaxRetries; attempt++ {
		if err := c.limiters.get(u.Host).Wait(ctx); err != nil {
			return nil, &Error{Kind: KindTransient, URL: rawURL, Err: err}
		}
		resp, err := c.do(ctx, u, false)
		if err == nil {
			resp.Attempts = attempt + 1
			resp.Duration = time.Since(start)
			return resp, nil
		}
		lastErr = err
		fe, _ := AsError(err)
		if fe == nil || !fe.Retryable() || attempt == c.opts.MaxRetries {
			break
		}
		delay := c.backoff(attempt)
		if fe.RetryAfter > 0 {
			if fe.RetryAfter > c.opts.MaxBackoff {
				break // too long to wait in-request; the scheduler will honor it
			}
			delay = fe.RetryAfter
		}
		if err := c.opts.sleep(ctx, delay); err != nil {
			break
		}
	}
	return nil, lastErr
}

// backoff is exponential with full jitter: uniform in [0, min(max, base*2^attempt)].
func (c *Client) backoff(attempt int) time.Duration {
	ceil := c.opts.BaseBackoff << attempt
	if ceil > c.opts.MaxBackoff || ceil <= 0 {
		ceil = c.opts.MaxBackoff
	}
	return time.Duration(rand.Int64N(int64(ceil) + 1))
}

// do performs a single HTTP GET. anyType disables the content-type check (robots.txt).
func (c *Client) do(ctx context.Context, u *url.URL, anyType bool) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Error{Kind: KindForbidden, URL: u.String(), Err: err}
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, classifyTransportError(u.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	out := &Response{URL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // allow connection reuse
		return nil, statusError(u.String(), resp)
	}
	if !anyType && !acceptableType(out.ContentType) {
		return nil, &Error{Kind: KindUnsupported, Status: resp.StatusCode, URL: u.String(),
			Err: fmt.Errorf("content type %q", out.ContentType)}
	}
	// The transport transparently decompresses gzip, so this cap bounds the
	// decompressed size and defeats compression bombs.
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxBody+1))
	if err != nil {
		return nil, classifyTransportError(u.String(), err)
	}
	if int64(len(body)) > c.opts.MaxBody {
		return nil, &Error{Kind: KindTooLarge, Status: resp.StatusCode, URL: u.String(),
			Err: fmt.Errorf("body exceeds %d bytes", c.opts.MaxBody)}
	}
	out.Body = body
	return out, nil
}

func acceptableType(ct string) bool {
	if ct == "" {
		return true // many small sites omit it; the parser copes with garbage
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch mt {
	case "text/html", "application/xhtml+xml", "application/json", "application/ld+json", "text/plain":
		return true
	}
	return false
}

func statusError(rawURL string, resp *http.Response) error {
	e := &Error{Status: resp.StatusCode, URL: rawURL}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		e.Kind = KindNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		e.Kind = KindBlocked
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case resp.StatusCode >= 500:
		e.Kind = KindTransient
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	default:
		e.Kind = KindHTTP
	}
	return e
}

func classifyTransportError(rawURL string, err error) error {
	switch {
	case errors.Is(err, ErrForbiddenTarget):
		return &Error{Kind: KindForbidden, URL: rawURL, Err: ErrForbiddenTarget}
	case errors.Is(err, errTooManyRedirects):
		return &Error{Kind: KindHTTP, URL: rawURL, Err: errTooManyRedirects}
	default:
		// Timeouts, resets, DNS failures, TLS errors: all worth retrying later.
		return &Error{Kind: KindTransient, URL: rawURL, Err: err}
	}
}

// parseRetryAfter accepts delta-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
