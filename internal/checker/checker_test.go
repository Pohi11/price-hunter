package checker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/extract"
	"github.com/Pohi11/price-hunter/internal/fetch"
	"github.com/Pohi11/price-hunter/internal/retailer"
	"github.com/Pohi11/price-hunter/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// fakeStore enforces the same conditions as the DynamoDB store: a check
// commits only while RUNNING, and a product update only at the expected version.
type fakeStore struct {
	mu            sync.Mutex
	checks        map[string]domain.CheckStatus
	products      map[string]domain.Product
	commits       []store.CheckCommit
	finishedOnly  []domain.Check
	health        map[string]domain.DomainHealth
	opened        int
	successWrites int
	beginErr      error
	commitHook    func(c store.CheckCommit) error // runs before applying a commit
}

func newFakeStore(products ...domain.Product) *fakeStore {
	fs := &fakeStore{checks: map[string]domain.CheckStatus{}, products: map[string]domain.Product{}, health: map[string]domain.DomainHealth{}}
	for _, p := range products {
		fs.products[p.ID] = p
	}
	return fs
}

func (f *fakeStore) BeginCheck(_ context.Context, msg domain.CheckMessage, _ time.Time, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginErr != nil {
		return false, f.beginErr
	}
	if s, ok := f.checks[msg.CheckID]; ok && s != domain.CheckQueued {
		return false, nil
	}
	f.checks[msg.CheckID] = domain.CheckRunning
	return true, nil
}

func (f *fakeStore) GetProduct(_ context.Context, _, id string) (domain.Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.products[id]
	if !ok {
		return domain.Product{}, domain.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) CommitCheck(_ context.Context, c store.CheckCommit) error {
	if f.commitHook != nil {
		if err := f.commitHook(c); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checks[c.Check.ID] != domain.CheckRunning {
		return store.ErrCheckNotRunning
	}
	if c.Product != nil {
		cur, ok := f.products[c.Product.ID]
		if !ok {
			return domain.ErrNotFound
		}
		if cur.Version != c.ExpectedVersion {
			return domain.ErrVersionConflict
		}
		f.products[c.Product.ID] = *c.Product
	}
	f.checks[c.Check.ID] = c.Check.Status
	f.commits = append(f.commits, c)
	return nil
}

func (f *fakeStore) FinishCheckOnly(_ context.Context, c domain.Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks[c.ID] = c.Status
	f.finishedOnly = append(f.finishedOnly, c)
	return nil
}

func (f *fakeStore) GetDomainHealth(_ context.Context, host string) (domain.DomainHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health[host], nil
}

func (f *fakeStore) RecordDomainFailure(_ context.Context, host string, o domain.OutcomeCode, _ time.Time) (domain.DomainHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.health[host]
	h.Host, h.ConsecutiveFailures, h.LastOutcome = host, h.ConsecutiveFailures+1, o
	f.health[host] = h
	return h, nil
}

func (f *fakeStore) OpenCircuit(_ context.Context, host string, until, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.health[host]
	if h.IsOpen(now) {
		return false, nil
	}
	h.OpenUntil, h.ConsecutiveFailures = &until, 0
	h.OpenCount++
	f.health[host] = h
	f.opened++
	return true, nil
}

func (f *fakeStore) RecordDomainSuccess(_ context.Context, host string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health[host] = domain.DomainHealth{Host: host}
	f.successWrites++
	return nil
}

type fakeSource struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, url string) (retailer.Quote, error)
}

func (s *fakeSource) Quote(ctx context.Context, url string) (retailer.Quote, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.fn(ctx, url)
}

func priceQuote(minor int64) func(context.Context, string) (retailer.Quote, error) {
	return func(context.Context, string) (retailer.Quote, error) {
		return retailer.Quote{Retailer: "generic", Offer: extract.Offer{
			Price: domain.Money{Minor: minor, Currency: "USD"}, Availability: domain.InStock, Strategy: "jsonld"}}, nil
	}
}

func failing(err error) func(context.Context, string) (retailer.Quote, error) {
	return func(context.Context, string) (retailer.Quote, error) {
		return retailer.Quote{Retailer: "generic", Body: []byte("<html>changed</html>")}, err
	}
}

type recorder struct {
	mu        sync.Mutex
	snapshots []string
	dispatch  []string
	reports   []Report
}

func (r *recorder) Save(_ context.Context, key string, _ []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, key)
	return nil
}

func (r *recorder) Dispatch(_ context.Context, _, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatch = append(r.dispatch, id)
	return nil
}

func (r *recorder) CheckCompleted(_ context.Context, rep Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, rep)
}

func product(id string) domain.Product {
	return domain.Product{ID: id, UserID: "u1", Name: "Air Fryer", URL: "https://shop.example.com/p/" + id,
		Retailer: "generic", Host: "shop.example.com", Target: domain.Money{Minor: 10000, Currency: "USD"},
		Frequency: domain.Frequency(6 * time.Hour), Status: domain.StatusActive, AlertState: domain.AlertArmed, Version: 1}
}

func msg(productID, checkID string, trig domain.Trigger) domain.CheckMessage {
	return domain.CheckMessage{UserID: "u1", ProductID: productID, CheckID: checkID, Trigger: trig, EnqueuedAt: t0}
}

func newChecker(fs *fakeStore, src *fakeSource, rec *recorder) *Checker {
	cfg := DefaultConfig()
	cfg.CheckTimeout = time.Second
	ids := 0
	return New(fs, src, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithSnapshots(rec), WithDispatcher(rec), WithObserver(rec),
		WithClock(func() time.Time { return t0 }), WithJitter(func(time.Duration) time.Duration { return 0 }),
		WithIDs(func() string { ids++; return "N" + string(rune('0'+ids)) }))
}

func TestSuccessfulCheckAlertsOnce(t *testing.T) {
	fs := newFakeStore(product("P1"))
	src := &fakeSource{fn: priceQuote(9400)}
	rec := &recorder{}
	c := newChecker(fs, src, rec)

	if err := c.Run(context.Background(), msg("P1", "C1", domain.TriggerScheduled)); err != nil {
		t.Fatal(err)
	}
	if len(fs.commits) != 1 {
		t.Fatalf("commits = %d", len(fs.commits))
	}
	cm := fs.commits[0]
	if cm.Check.Status != domain.CheckSucceeded || cm.Point == nil || cm.Point.CheckID != "C1" || cm.Point.ProductID != "P1" {
		t.Fatalf("commit = %+v", cm)
	}
	if cm.Notification == nil || cm.Notification.Price.Minor != 9400 || len(rec.dispatch) != 1 {
		t.Fatalf("alert not raised/dispatched: %+v %v", cm.Notification, rec.dispatch)
	}
	if got := fs.products["P1"]; got.AlertState != domain.AlertTriggered || got.Current.Minor != 9400 {
		t.Fatalf("product = %+v", got)
	}

	// Next check, still below target: no second alert.
	if err := c.Run(context.Background(), msg("P1", "C2", domain.TriggerScheduled)); err != nil {
		t.Fatal(err)
	}
	if fs.commits[1].Notification != nil || len(rec.dispatch) != 1 {
		t.Fatal("alerted twice for one crossing")
	}
}

func TestDuplicateDeliveryDoesNotFetch(t *testing.T) {
	fs := newFakeStore(product("P1"))
	src := &fakeSource{fn: priceQuote(9400)}
	rec := &recorder{}
	c := newChecker(fs, src, rec)
	m := msg("P1", "C1", domain.TriggerScheduled)
	_ = c.Run(context.Background(), m)
	if err := c.Run(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if src.calls != 1 || len(fs.commits) != 1 || !rec.reports[1].Duplicate {
		t.Fatalf("duplicate processed: calls=%d commits=%d", src.calls, len(fs.commits))
	}
}

func TestCommitRaceLostToDuplicate(t *testing.T) {
	fs := newFakeStore(product("P1"))
	fs.commitHook = func(store.CheckCommit) error { return store.ErrCheckNotRunning }
	rec := &recorder{}
	c := newChecker(fs, &fakeSource{fn: priceQuote(9400)}, rec)
	if err := c.Run(context.Background(), msg("P1", "C1", domain.TriggerScheduled)); err != nil {
		t.Fatalf("lost race must not be an error: %v", err)
	}
	if !rec.reports[0].Duplicate || len(rec.dispatch) != 0 {
		t.Fatal("lost race should be reported as duplicate and must not dispatch")
	}
}

func TestDeletedAndPausedProducts(t *testing.T) {
	paused := product("P2")
	paused.Status = domain.StatusPaused
	fs := newFakeStore(paused)
	src := &fakeSource{fn: priceQuote(9400)}
	c := newChecker(fs, src, &recorder{})

	_ = c.Run(context.Background(), msg("GONE", "C1", domain.TriggerScheduled))
	_ = c.Run(context.Background(), msg("P2", "C2", domain.TriggerScheduled))
	if src.calls != 0 || len(fs.finishedOnly) != 2 || fs.finishedOnly[0].Outcome != domain.OutcomeProductInactive {
		t.Fatalf("inactive products fetched: calls=%d finished=%+v", src.calls, fs.finishedOnly)
	}
	// A manual check of a paused product still runs.
	_ = c.Run(context.Background(), msg("P2", "C3", domain.TriggerManual))
	if src.calls != 1 {
		t.Fatal("manual check of paused product did not run")
	}
}

func TestExtractionFailureSnapshotsPage(t *testing.T) {
	fs := newFakeStore(product("P1"))
	rec := &recorder{}
	c := newChecker(fs, &fakeSource{fn: failing(extract.ErrNoPrice)}, rec)
	if err := c.Run(context.Background(), msg("P1", "C1", domain.TriggerScheduled)); err != nil {
		t.Fatal(err)
	}
	if fs.commits[0].Check.Status != domain.CheckFailed || fs.commits[0].Check.Outcome != domain.OutcomePriceNotFound {
		t.Fatalf("check = %+v", fs.commits[0].Check)
	}
	if len(rec.snapshots) != 1 || !strings.HasPrefix(rec.snapshots[0], "snapshots/generic/2026-10-01/P1-C1") {
		t.Fatalf("snapshots = %v", rec.snapshots)
	}
	if fs.products["P1"].ConsecutiveFailures != 1 {
		t.Fatal("failure not counted")
	}
}

func TestVersionConflictReevaluatesAgainstNewSettings(t *testing.T) {
	fs := newFakeStore(product("P1"))
	once := false
	fs.commitHook = func(store.CheckCommit) error {
		if !once {
			once = true
			// The user lowers the target to $90 mid-check.
			fs.mu.Lock()
			p := fs.products["P1"]
			p.Target, p.Version = domain.Money{Minor: 9000, Currency: "USD"}, 2
			fs.products["P1"] = p
			fs.mu.Unlock()
		}
		return nil
	}
	rec := &recorder{}
	c := newChecker(fs, &fakeSource{fn: priceQuote(9400)}, rec)
	if err := c.Run(context.Background(), msg("P1", "C1", domain.TriggerScheduled)); err != nil {
		t.Fatal(err)
	}
	// $94 is above the new $90 target: no alert, and the new target is kept.
	cm := fs.commits[len(fs.commits)-1]
	if cm.Notification != nil || cm.ExpectedVersion != 2 || fs.products["P1"].Target.Minor != 9000 {
		t.Fatalf("stale settings used: notif=%v version=%d", cm.Notification, cm.ExpectedVersion)
	}
	if rec.reports[0].VersionRetries != 1 {
		t.Fatalf("retries = %d", rec.reports[0].VersionRetries)
	}
}

func TestInfrastructureErrorsAreReturned(t *testing.T) {
	fs := newFakeStore(product("P1"))
	fs.beginErr = errors.New("dynamodb: throttled")
	c := newChecker(fs, &fakeSource{fn: priceQuote(1)}, &recorder{})
	if err := c.Run(context.Background(), msg("P1", "C1", domain.TriggerScheduled)); err == nil {
		t.Fatal("infrastructure error swallowed; message would be lost")
	}
}

func TestCancelledContextLeavesMessageForRedelivery(t *testing.T) {
	fs := newFakeStore(product("P1"))
	ctx, cancel := context.WithCancel(context.Background())
	src := &fakeSource{fn: func(ctx context.Context, _ string) (retailer.Quote, error) {
		cancel() // shutdown mid-fetch
		return retailer.Quote{}, &fetch.Error{Kind: fetch.KindTransient, Err: ctx.Err()}
	}}
	c := newChecker(fs, src, &recorder{})
	if err := c.Run(ctx, msg("P1", "C1", domain.TriggerScheduled)); err == nil {
		t.Fatal("interrupted check must return an error")
	}
	if len(fs.commits) != 0 {
		t.Fatal("interrupted check committed a misleading outcome")
	}
}

func TestCircuitBreakerOpensAndSkips(t *testing.T) {
	var ps []domain.Product
	for i := 0; i < 12; i++ {
		ps = append(ps, product("P"+string(rune('A'+i))))
	}
	fs := newFakeStore(ps...)
	src := &fakeSource{fn: failing(&fetch.Error{Kind: fetch.KindBlocked, Status: 403})}
	rec := &recorder{}
	c := newChecker(fs, src, rec)

	for i := 0; i < 10; i++ {
		_ = c.Run(context.Background(), msg(ps[i].ID, "C"+ps[i].ID, domain.TriggerScheduled))
	}
	if fs.opened != 1 || !rec.reports[9].CircuitOpened {
		t.Fatalf("circuit not opened after 10 failures (opened=%d)", fs.opened)
	}
	calls := src.calls

	// Scheduled check of another product on the same host: skipped, no fetch.
	_ = c.Run(context.Background(), msg(ps[10].ID, "X1", domain.TriggerScheduled))
	last := fs.commits[len(fs.commits)-1]
	if src.calls != calls || last.Check.Status != domain.CheckSkipped || last.Check.Outcome != domain.OutcomeCircuitOpen {
		t.Fatalf("open circuit not honored: %+v", last.Check)
	}
	if next := last.Product.NextCheckAt; next == nil || next.Before(t0.Add(15*time.Minute)) {
		t.Fatalf("rescheduled before circuit closes: %v", next)
	}
	// Manual checks bypass the breaker.
	_ = c.Run(context.Background(), msg(ps[11].ID, "M1", domain.TriggerManual))
	if src.calls != calls+1 {
		t.Fatal("manual check blocked by circuit")
	}
}

func TestSuccessResetsUnhealthyDomainOnly(t *testing.T) {
	fs := newFakeStore(product("P1"), product("P2"))
	fs.health["shop.example.com"] = domain.DomainHealth{Host: "shop.example.com", ConsecutiveFailures: 3}
	c := newChecker(fs, &fakeSource{fn: priceQuote(12000)}, &recorder{})
	_ = c.Run(context.Background(), msg("P1", "C1", domain.TriggerScheduled))
	_ = c.Run(context.Background(), msg("P2", "C2", domain.TriggerScheduled))
	if fs.successWrites != 1 {
		t.Fatalf("success writes = %d, want 1 (only when the domain was unhealthy)", fs.successWrites)
	}
}
