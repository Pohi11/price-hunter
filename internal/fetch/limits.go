package fetch

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// hostLimiters holds one token bucket per host. In Lambda this is
// per-instance; the global bound per host is
// (event-source max concurrency) x (rate per second). See ADR-006.
type hostLimiters struct {
	mu       sync.Mutex
	m        map[string]*rate.Limiter
	r        rate.Limit
	burst    int
	hostRate func(host string) float64
}

func newHostLimiters(perSecond float64, burst int, hostRate func(string) float64) *hostLimiters {
	return &hostLimiters{m: map[string]*rate.Limiter{}, r: rate.Limit(perSecond), burst: burst, hostRate: hostRate}
}

func (h *hostLimiters) get(host string) *rate.Limiter {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.m[host]
	if !ok {
		r := h.r
		if h.hostRate != nil {
			if hr := h.hostRate(host); hr > 0 {
				r = rate.Limit(hr)
			}
		}
		l = rate.NewLimiter(r, h.burst)
		h.m[host] = l
	}
	return l
}

// robotsCache fetches and caches robots.txt per scheme+host.
type robotsCache struct {
	client *Client
	ttl    time.Duration
	group  singleflight.Group

	mu      sync.Mutex
	entries map[string]robotsEntry
}

type robotsEntry struct {
	data    *robotstxt.RobotsData
	expires time.Time
}

func newRobotsCache(c *Client, ttl time.Duration) *robotsCache {
	return &robotsCache{client: c, ttl: ttl, entries: map[string]robotsEntry{}}
}

// robotsAgent is the product token matched against User-agent lines.
const robotsAgent = "PriceHunter"

func (r *robotsCache) allowed(ctx context.Context, u *url.URL) (bool, error) {
	key := u.Scheme + "://" + u.Host
	r.mu.Lock()
	e, ok := r.entries[key]
	r.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.data.TestAgent(pathOf(u), robotsAgent), nil
	}

	v, err, _ := r.group.Do(key, func() (any, error) {
		return r.load(ctx, key)
	})
	if err != nil {
		return false, err
	}
	return v.(*robotstxt.RobotsData).TestAgent(pathOf(u), robotsAgent), nil
}

func (r *robotsCache) load(ctx context.Context, key string) (*robotstxt.RobotsData, error) {
	ru, _ := url.Parse(key + "/robots.txt")
	if err := r.client.limiters.get(ru.Host).Wait(ctx); err != nil {
		return nil, &Error{Kind: KindTransient, URL: ru.String(), Err: err}
	}
	resp, err := r.client.do(ctx, ru, true)
	status := 200
	var body []byte
	if err != nil {
		fe, ok := AsError(err)
		switch {
		case ok && fe.Status >= 400 && fe.Status < 500:
			status = fe.Status // RFC 9309: 4xx means no restrictions
		case ok && fe.Kind == KindForbidden:
			return nil, err
		default:
			// 5xx or unreachable: don't guess; let the check fail as transient
			// and try again later rather than caching a verdict.
			return nil, &Error{Kind: KindTransient, URL: ru.String(), Err: errors.New("robots.txt unavailable")}
		}
	} else {
		body = resp.Body
		if len(body) > 500<<10 {
			body = body[:500<<10] // RFC 9309 parse limit
		}
	}
	data, perr := robotstxt.FromStatusAndBytes(status, body)
	if perr != nil {
		data, _ = robotstxt.FromStatusAndBytes(404, nil) // unparseable: treat as absent
	}
	r.mu.Lock()
	r.entries[key] = robotsEntry{data: data, expires: time.Now().Add(r.ttl)}
	r.mu.Unlock()
	return data, nil
}

func pathOf(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}
