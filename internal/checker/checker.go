// Package checker runs one price check end to end:
//
//	BeginCheck (idempotency guard)
//	  -> load product -> circuit breaker -> fetch + extract
//	  -> domain.Policy.Apply (pure state transition)
//	  -> snapshot failed pages -> CommitCheck (one transaction)
//	  -> circuit bookkeeping -> optional inline notification dispatch
//
// Run returns an error only for infrastructure failures (the queue should
// redeliver). Retailer failures are recorded as outcomes and return nil.
package checker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/retailer"
	"github.com/Pohi11/price-hunter/internal/store"
	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// Store is the persistence a check needs.
type Store interface {
	BeginCheck(ctx context.Context, msg domain.CheckMessage, now time.Time, staleAfter time.Duration) (bool, error)
	GetProduct(ctx context.Context, userID, productID string) (domain.Product, error)
	CommitCheck(ctx context.Context, c store.CheckCommit) error
	FinishCheckOnly(ctx context.Context, c domain.Check) error
	GetDomainHealth(ctx context.Context, host string) (domain.DomainHealth, error)
	RecordDomainFailure(ctx context.Context, host string, outcome domain.OutcomeCode, now time.Time) (domain.DomainHealth, error)
	OpenCircuit(ctx context.Context, host string, until, now time.Time) (bool, error)
	RecordDomainSuccess(ctx context.Context, host string, now time.Time) error
}

// Source fetches and extracts a price.
type Source interface {
	Quote(ctx context.Context, rawURL string) (retailer.Quote, error)
}

// Snapshotter stores the HTML of pages whose extraction failed, so parser
// drift can be diagnosed and turned into a golden test fixture.
type Snapshotter interface {
	Save(ctx context.Context, key string, body []byte) error
}

// Dispatcher delivers an outbox notification. In AWS this happens via
// DynamoDB Streams; locally the checker calls it inline after commit.
type Dispatcher interface {
	Dispatch(ctx context.Context, userID, notificationID string) error
}

// Observer receives a report per check (metrics; see package telemetry).
type Observer interface {
	CheckCompleted(ctx context.Context, r Report)
}

// Report summarizes one finished (or skipped) check.
type Report struct {
	Outcome        domain.OutcomeCode
	Status         domain.CheckStatus
	Retailer       string
	Strategy       string
	Duplicate      bool // another delivery already handled this check
	Alert          bool
	Suspect        bool
	Disagreement   bool
	FetchDuration  time.Duration
	TotalDuration  time.Duration
	CircuitOpened  bool
	VersionRetries int
}

// CircuitConfig tunes the per-retailer circuit breaker.
type CircuitConfig struct {
	Threshold int           // consecutive retailer failures (across products) before opening
	BaseOpen  time.Duration // first open window; doubles each time it reopens
	MaxOpen   time.Duration
	CacheTTL  time.Duration // how long a worker trusts its cached health read
}

// Config tunes the checker.
type Config struct {
	Policy       domain.Policy
	CheckTimeout time.Duration // fetch + extract budget
	// StaleAfter is when a RUNNING check is considered abandoned (crashed
	// worker). It must be shorter than the queue's visibility timeout so a
	// redelivery can take over.
	StaleAfter time.Duration
	Circuit    CircuitConfig
}

// DefaultConfig returns production settings.
func DefaultConfig() Config {
	return Config{
		Policy:       domain.DefaultPolicy(),
		CheckTimeout: 25 * time.Second,
		StaleAfter:   2 * time.Minute,
		Circuit:      CircuitConfig{Threshold: 10, BaseOpen: 15 * time.Minute, MaxOpen: 6 * time.Hour, CacheTTL: 30 * time.Second},
	}
}

// Checker executes checks. Safe for concurrent use.
type Checker struct {
	store      Store
	source     Source
	snapshots  Snapshotter
	dispatcher Dispatcher
	observer   Observer
	cfg        Config
	log        *slog.Logger
	health     *healthCache

	now    func() time.Time
	jitter domain.Jitter
	newID  func() string
}

// Option configures optional collaborators.
type Option func(*Checker)

func WithSnapshots(s Snapshotter) Option    { return func(c *Checker) { c.snapshots = s } }
func WithDispatcher(d Dispatcher) Option    { return func(c *Checker) { c.dispatcher = d } }
func WithObserver(o Observer) Option        { return func(c *Checker) { c.observer = o } }
func WithClock(now func() time.Time) Option { return func(c *Checker) { c.now = now } }
func WithJitter(j domain.Jitter) Option     { return func(c *Checker) { c.jitter = j } }
func WithIDs(newID func() string) Option    { return func(c *Checker) { c.newID = newID } }

// New builds a Checker.
func New(s Store, src Source, cfg Config, log *slog.Logger, opts ...Option) *Checker {
	c := &Checker{store: s, source: src, cfg: cfg, log: log,
		now: func() time.Time { return time.Now().UTC() }, jitter: domain.RandomJitter,
		newID: func() string { return ulid.Make().String() }}
	for _, o := range opts {
		o(c)
	}
	c.health = newHealthCache(cfg.Circuit.CacheTTL, c.now)
	return c
}

// maxVersionRetries bounds re-evaluation when the user edits a product mid-check.
const maxVersionRetries = 3

// Run executes the check described by msg.
func (c *Checker) Run(ctx context.Context, msg domain.CheckMessage) error {
	start := c.now()
	ctx = telemetry.WithAttrs(ctx, slog.String("check_id", msg.CheckID), slog.String("product_id", msg.ProductID),
		slog.String("trigger", string(msg.Trigger)))
	report := Report{}
	defer func() {
		report.TotalDuration = c.now().Sub(start)
		if c.observer != nil {
			c.observer.CheckCompleted(ctx, report)
		}
	}()

	begun, err := c.store.BeginCheck(ctx, msg, start, c.cfg.StaleAfter)
	if err != nil {
		return err
	}
	if !begun {
		report.Duplicate = true
		c.log.InfoContext(ctx, "duplicate delivery skipped")
		return nil
	}

	p, err := c.store.GetProduct(ctx, msg.UserID, msg.ProductID)
	if errors.Is(err, domain.ErrNotFound) {
		c.log.InfoContext(ctx, "check skipped: product no longer exists")
		report.Outcome, report.Status = domain.OutcomeProductInactive, domain.CheckSkipped
		return c.store.FinishCheckOnly(ctx, c.finishedCheck(msg, start, domain.CheckSkipped, domain.OutcomeProductInactive, nil, "", false))
	}
	if err != nil {
		return err
	}
	report.Retailer = p.Retailer
	if !p.Status.Schedulable() && msg.Trigger != domain.TriggerManual {
		c.log.InfoContext(ctx, "check skipped: product not schedulable", "status", p.Status)
		report.Outcome, report.Status = domain.OutcomeProductInactive, domain.CheckSkipped
		return c.store.FinishCheckOnly(ctx, c.finishedCheck(msg, start, domain.CheckSkipped, domain.OutcomeProductInactive, nil, "", false))
	}

	in, quote, qerr := c.observe(ctx, p, msg, start)
	if ctx.Err() != nil {
		// Shutting down or out of time: don't record a misleading outcome.
		// Returning an error leaves the message for redelivery.
		return fmt.Errorf("check interrupted: %w", ctx.Err())
	}
	report.FetchDuration, report.Strategy, report.Disagreement = quote.FetchDuration, quote.Offer.Strategy, quote.Disagreement
	if in.Outcome.IsExtractionFailure() || in.Outcome == domain.OutcomeUnsupportedContent {
		c.snapshot(ctx, p, msg.CheckID, quote.Body)
	}

	var tr domain.Transition
	for attempt := 0; ; attempt++ {
		tr = c.cfg.Policy.Apply(p, in, c.jitter)
		err = c.commit(ctx, msg, start, p, tr, qerr)
		if err == nil {
			break
		}
		switch {
		case errors.Is(err, store.ErrCheckNotRunning):
			report.Duplicate = true
			return nil
		case errors.Is(err, domain.ErrNotFound):
			report.Outcome, report.Status = domain.OutcomeProductInactive, domain.CheckSkipped
			return c.store.FinishCheckOnly(ctx, c.finishedCheck(msg, start, domain.CheckSkipped, domain.OutcomeProductInactive, nil, "", false))
		case errors.Is(err, domain.ErrVersionConflict) && attempt < maxVersionRetries:
			// The user edited the product mid-check (e.g. new target).
			// Re-evaluate the same observation against the new settings.
			report.VersionRetries++
			if p, err = c.store.GetProduct(ctx, msg.UserID, msg.ProductID); err != nil {
				return err
			}
			continue
		default:
			return err
		}
	}
	report.Outcome, report.Status, report.Alert, report.Suspect = tr.Outcome, tr.CheckStatus, tr.Alert, tr.Suspect
	report.CircuitOpened = c.recordRetailerHealth(ctx, p.Host, tr.Outcome)

	c.log.InfoContext(ctx, "check completed",
		"outcome", tr.Outcome, "status", tr.CheckStatus, "retailer", p.Retailer, "strategy", quote.Offer.Strategy,
		"alert", tr.Alert, "suspect", tr.Suspect, "fetch_ms", quote.FetchDuration.Milliseconds(),
		"total_ms", c.now().Sub(start).Milliseconds(), "next_check_at", tr.Product.NextCheckAt)
	return nil
}

// observe produces the policy input: either a circuit-open skip or the
// result of fetching the page.
func (c *Checker) observe(ctx context.Context, p domain.Product, msg domain.CheckMessage, now time.Time) (domain.CheckInput, retailer.Quote, error) {
	if msg.Trigger != domain.TriggerManual {
		if h := c.healthOf(ctx, p.Host); h.IsOpen(now) {
			return domain.CheckInput{Outcome: domain.OutcomeCircuitOpen, Now: now, RetryAfter: h.OpenUntil.Sub(now)}, retailer.Quote{Retailer: p.Retailer}, nil
		}
	}
	qctx, cancel := context.WithTimeout(ctx, c.cfg.CheckTimeout)
	defer cancel()
	q, err := c.source.Quote(qctx, p.URL)
	outcome, retryAfter := retailer.Classify(err)
	in := domain.CheckInput{Outcome: outcome, Now: c.now(), RetryAfter: retryAfter}
	if err == nil {
		in.Observation = &domain.Observation{Price: q.Offer.Price, Availability: q.Offer.Availability, Strategy: q.Offer.Strategy}
	} else {
		c.log.InfoContext(ctx, "check failed", "outcome", outcome, "err", err)
	}
	return in, q, err
}

func (c *Checker) commit(ctx context.Context, msg domain.CheckMessage, start time.Time, p domain.Product, tr domain.Transition, qerr error) error {
	finished := c.now()
	tr.Product.UpdatedAt = finished
	var price *domain.Money
	if tr.Point != nil {
		pp := tr.Point.Price
		price = &pp
	}
	strategy := ""
	if tr.Point != nil {
		strategy = tr.Point.Strategy
	}
	detail := ""
	if qerr != nil {
		detail = qerr.Error()
	}
	cc := store.CheckCommit{
		Check:           c.finishedCheck(msg, start, tr.CheckStatus, tr.Outcome, price, strategy, tr.Alert),
		Product:         &tr.Product,
		ExpectedVersion: p.Version,
	}
	cc.Check.Message = detail
	cc.Check.FinishedAt = &finished
	if tr.Point != nil {
		pt := *tr.Point
		pt.ProductID, pt.CheckID = p.ID, msg.CheckID
		cc.Point = &pt
	}
	if tr.Alert {
		cc.Notification = &domain.Notification{
			ID: c.newID(), UserID: p.UserID, ProductID: p.ID, ProductName: p.Name, ProductURL: p.URL,
			Price: *tr.Product.Current, Target: tr.Product.Target, Status: domain.NotificationPending, CreatedAt: finished,
		}
	}
	if err := c.store.CommitCheck(ctx, cc); err != nil {
		return err
	}
	if cc.Notification != nil && c.dispatcher != nil {
		if err := c.dispatcher.Dispatch(ctx, p.UserID, cc.Notification.ID); err != nil {
			c.log.WarnContext(ctx, "inline notification dispatch failed; it stays PENDING", "notification_id", cc.Notification.ID, "err", err)
		}
	}
	return nil
}

func (c *Checker) finishedCheck(msg domain.CheckMessage, start time.Time, status domain.CheckStatus, outcome domain.OutcomeCode, price *domain.Money, strategy string, alerted bool) domain.Check {
	finished := c.now()
	return domain.Check{
		ID: msg.CheckID, ProductID: msg.ProductID, UserID: msg.UserID, Status: status, Trigger: msg.Trigger,
		FinishedAt: &finished, DurationMS: finished.Sub(start).Milliseconds(), Outcome: outcome,
		Price: price, Strategy: strategy, Alerted: alerted,
	}
}

func (c *Checker) snapshot(ctx context.Context, p domain.Product, checkID string, body []byte) {
	if c.snapshots == nil || len(body) == 0 {
		return
	}
	key := fmt.Sprintf("snapshots/%s/%s/%s-%s.html", p.Retailer, c.now().Format("2006-01-02"), p.ID, checkID)
	if err := c.snapshots.Save(ctx, key, body); err != nil {
		c.log.WarnContext(ctx, "snapshot failed", "key", key, "err", err)
		return
	}
	c.log.InfoContext(ctx, "saved page snapshot for failed extraction", "key", key)
}
