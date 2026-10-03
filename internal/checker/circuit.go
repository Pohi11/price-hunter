package checker

import (
	"context"
	"sync"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// The circuit breaker is per retailer host and shared across all workers
// through DynamoDB (Lambda instances share no memory). Each worker caches
// its last read briefly to avoid a read per check.
//
//	closed --(Threshold consecutive retailer failures, any product)--> open
//	open   --(OpenUntil passes)--> half-open: the next check is a probe
//	probe fails --> re-open for twice as long (capped at MaxOpen)
//	probe succeeds --> closed, counters reset
//
// Manual checks bypass an open circuit so a user can always see what's happening.

type healthCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]healthEntry
}

type healthEntry struct {
	h       domain.DomainHealth
	fetched time.Time
}

func newHealthCache(ttl time.Duration, now func() time.Time) *healthCache {
	return &healthCache{ttl: ttl, now: now, entries: map[string]healthEntry{}}
}

func (hc *healthCache) get(host string) (domain.DomainHealth, bool) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	e, ok := hc.entries[host]
	if !ok || hc.now().Sub(e.fetched) > hc.ttl {
		return domain.DomainHealth{}, false
	}
	return e.h, true
}

func (hc *healthCache) put(h domain.DomainHealth) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.entries[h.Host] = healthEntry{h: h, fetched: hc.now()}
}

// healthOf returns the (possibly cached) health of host. Read errors are
// treated as "closed": the breaker must never be the reason checks stop.
func (c *Checker) healthOf(ctx context.Context, host string) domain.DomainHealth {
	if h, ok := c.health.get(host); ok {
		return h
	}
	h, err := c.store.GetDomainHealth(ctx, host)
	if err != nil {
		c.log.WarnContext(ctx, "domain health read failed; assuming closed", "host", host, "err", err)
		return domain.DomainHealth{Host: host}
	}
	h.Host = host
	c.health.put(h)
	return h
}

// recordRetailerHealth updates the shared breaker after a check. Returns
// true if this call opened the circuit.
func (c *Checker) recordRetailerHealth(ctx context.Context, host string, outcome domain.OutcomeCode) bool {
	now := c.now()
	switch {
	case outcome.CountsAgainstRetailer():
		h, err := c.store.RecordDomainFailure(ctx, host, outcome, now)
		if err != nil {
			c.log.WarnContext(ctx, "record domain failure", "host", host, "err", err)
			return false
		}
		if h.ConsecutiveFailures < c.cfg.Circuit.Threshold {
			c.health.put(h)
			return false
		}
		window := c.cfg.Circuit.BaseOpen << min(h.OpenCount, 10)
		if window > c.cfg.Circuit.MaxOpen || window <= 0 {
			window = c.cfg.Circuit.MaxOpen
		}
		until := now.Add(window)
		opened, err := c.store.OpenCircuit(ctx, host, until, now)
		if err != nil {
			c.log.WarnContext(ctx, "open circuit", "host", host, "err", err)
			return false
		}
		if opened {
			h.OpenUntil, h.ConsecutiveFailures = &until, 0
			h.OpenCount++
			c.health.put(h)
			c.log.WarnContext(ctx, "circuit opened for retailer", "host", host, "until", until, "window", window.String())
		}
		return opened
	case outcome == domain.OutcomeOK:
		if h, ok := c.health.get(host); ok && h.ConsecutiveFailures == 0 && h.OpenCount == 0 {
			return false // known healthy: skip the write
		}
		if err := c.store.RecordDomainSuccess(ctx, host, now); err != nil {
			c.log.WarnContext(ctx, "record domain success", "host", host, "err", err)
			return false
		}
		c.health.put(domain.DomainHealth{Host: host, LastOutcome: domain.OutcomeOK, UpdatedAt: now})
	}
	return false
}
