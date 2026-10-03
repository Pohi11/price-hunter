//go:build integration

// Package e2e exercises the full tracking loop against DynamoDB Local and an
// in-process demo store: create -> schedule -> queue -> check -> alert.
package e2e_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/checker"
	"github.com/Pohi11/price-hunter/internal/demostore"
	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/fetch"
	"github.com/Pohi11/price-hunter/internal/notify"
	"github.com/Pohi11/price-hunter/internal/products"
	"github.com/Pohi11/price-hunter/internal/queue"
	"github.com/Pohi11/price-hunter/internal/retailer"
	"github.com/Pohi11/price-hunter/internal/scheduler"
	"github.com/Pohi11/price-hunter/internal/store"
	"github.com/Pohi11/price-hunter/internal/testutil"
	"github.com/Pohi11/price-hunter/internal/urlx"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type outbox struct {
	mu   sync.Mutex
	sent []notify.Email
}

func (o *outbox) Send(_ context.Context, e notify.Email) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, e)
	return "test-msg", nil
}

func (o *outbox) count() int { o.mu.Lock(); defer o.mu.Unlock(); return len(o.sent) }

type snapshots struct {
	mu   sync.Mutex
	keys []string
}

func (s *snapshots) Save(_ context.Context, key string, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	return nil
}

type world struct {
	t     *testing.T
	clk   *clock
	store *store.Store
	shop  *httptest.Server
	q     *queue.Mem
	svc   *products.Service
	sched *scheduler.Scheduler
	check *checker.Checker
	mail  *outbox
	snaps *snapshots
}

func newWorld(t *testing.T) *world {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := &world{t: t, clk: &clock{t: time.Date(2026, 10, 1, 12, 7, 0, 0, time.UTC)}, mail: &outbox{}, snaps: &snapshots{}}
	db, table := testutil.NewTable(t)
	w.store = store.New(db, table)
	w.shop = httptest.NewServer(demostore.New(demostore.Options{Now: w.clk.Now, AdminToken: "t"}))
	t.Cleanup(w.shop.Close)

	reg, err := retailer.Default(retailer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := fetch.New(fetch.Options{Guard: fetch.Guard{AllowLoopback: true}, Timeout: 2 * time.Second,
		RespectRobots: true, HostRate: reg.RateFor, MaxRetries: -1})
	w.q = queue.NewMem(1000, 5)
	w.svc = products.New(w.store, w.q, reg, products.Config{URLOptions: urlx.Options{AllowLocalhost: true}}, log)
	w.svc.SetClock(w.clk.Now, nil)
	w.sched = scheduler.New(w.store, w.q, w.q, nil, scheduler.Config{}, log)
	w.sched.SetClock(w.clk.Now)
	dispatcher := notify.NewDispatcher(w.store, w.mail, nil, "http://app.test", log)
	cfg := checker.DefaultConfig()
	cfg.CheckTimeout = 3 * time.Second
	w.check = checker.New(w.store, retailer.NewSource(fetcher, reg), cfg, log,
		checker.WithDispatcher(dispatcher), checker.WithSnapshots(w.snaps), checker.WithClock(w.clk.Now),
		checker.WithJitter(func(time.Duration) time.Duration { return 0 }))
	return w
}

// drain processes everything currently queued, the way workers would.
func (w *world) drain() {
	w.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.q.Consume(ctx, queue.ConsumeOptions{Workers: 4, Drain: 5 * time.Second}, w.check.Run)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if d, _ := w.q.Depth(ctx); d == 0 {
			break
		}
		if time.Now().After(deadline) {
			w.t.Fatal("queue did not drain")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let in-flight handlers finish
	cancel()
	<-done
}

func (w *world) track(slug, target string) domain.Product {
	w.t.Helper()
	p, _, err := w.svc.Create(context.Background(), "alice", "alice@example.com", products.CreateInput{
		Name: slug, URL: w.shop.URL + "/p/" + slug, Target: products.MoneyInput{Amount: target, Currency: "USD"}, Frequency: "6h"})
	if err != nil {
		w.t.Fatalf("create %s: %v", slug, err)
	}
	return p
}

func (w *world) product(id string) domain.Product {
	p, err := w.store.GetProduct(context.Background(), "alice", id)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) override(slug, body string) {
	req, _ := http.NewRequest(http.MethodPut, w.shop.URL+"/admin/products/"+slug, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		w.t.Fatalf("override %s: %v %v", slug, err, resp)
	}
	_ = resp.Body.Close()
}

func (w *world) history(id string) []domain.PricePoint {
	pts, _, err := w.store.ListPrices(context.Background(), store.PriceQuery{ProductID: id,
		From: w.clk.Now().Add(-30 * 24 * time.Hour), To: w.clk.Now().Add(time.Hour), Limit: 100})
	if err != nil {
		w.t.Fatal(err)
	}
	return pts
}

func TestLoopFirstCheckAlertsOnceAndSchedules(t *testing.T) {
	w := newWorld(t)
	shopPrice := mustFind(t, "air-fryer").PriceAt(w.clk.Now())
	p := w.track("air-fryer", "200.00") // target well above the current price
	w.drain()

	got := w.product(p.ID)
	if got.Current == nil || *got.Current != shopPrice || got.AlertState != domain.AlertTriggered {
		t.Fatalf("after first check: current=%v alert=%s", got.Current, got.AlertState)
	}
	if w.mail.count() != 1 || !strings.Contains(w.mail.sent[0].Subject, "is now $"+shopPrice.Decimal()) {
		t.Fatalf("emails = %+v", w.mail.sent)
	}
	if want := w.clk.Now().Add(6 * time.Hour); !got.NextCheckAt.Equal(want) {
		t.Fatalf("next check %v, want %v", got.NextCheckAt, want)
	}

	// Nothing is due yet.
	if st, _ := w.sched.Run(context.Background()); st.Enqueued != 0 {
		t.Fatalf("scheduled too early: %+v", st)
	}
	// Six hours later the scheduler picks it up; the price is still below
	// target, so the history grows but no second email is sent.
	w.clk.Advance(6*time.Hour + time.Minute)
	if st, _ := w.sched.Run(context.Background()); st.Enqueued != 1 {
		t.Fatalf("not scheduled when due: %+v", st)
	}
	w.drain()
	if n := len(w.history(p.ID)); n != 2 {
		t.Fatalf("history points = %d, want 2", n)
	}
	if w.mail.count() != 1 {
		t.Fatalf("emails = %d, want 1", w.mail.count())
	}
}

func TestLoopDuplicateMessageIsHarmless(t *testing.T) {
	w := newWorld(t)
	p := w.track("air-fryer", "200.00")
	// Capture the initial message and deliver it twice (SQS is at-least-once).
	var first domain.CheckMessage
	w.q.Consume(ctxWithTimeout(t, 300*time.Millisecond), queue.ConsumeOptions{Workers: 1}, func(_ context.Context, m domain.CheckMessage) error {
		first = m
		return nil
	})
	_ = w.q.Enqueue(context.Background(), first, first)
	w.drain()
	if n := len(w.history(p.ID)); n != 1 {
		t.Fatalf("duplicate delivery wrote %d price points", n)
	}
	if w.mail.count() != 1 {
		t.Fatalf("duplicate delivery sent %d emails", w.mail.count())
	}
}

func TestLoopParserDriftLeadsToNeedsAttention(t *testing.T) {
	w := newWorld(t)
	p := w.track("air-fryer", "50.00")
	w.drain()
	w.override("air-fryer", `{"fault":"noprice"}`)
	for i := 0; i < 3; i++ {
		w.clk.Advance(25 * time.Hour) // past any backoff
		if _, err := w.sched.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.drain()
	}
	got := w.product(p.ID)
	if got.Status != domain.StatusNeedsAttention || got.LastErrorCode != domain.OutcomePriceNotFound || got.ConsecutiveFailures != 3 {
		t.Fatalf("product = status %s err %s failures %d", got.Status, got.LastErrorCode, got.ConsecutiveFailures)
	}
	if len(w.snaps.keys) != 3 || !strings.Contains(w.snaps.keys[0], "snapshots/demostore/") {
		t.Fatalf("snapshots = %v", w.snaps.keys)
	}
	// The retailer fixes the page: the next success restores ACTIVE.
	w.override("air-fryer", `{"fault":"none"}`)
	w.clk.Advance(25 * time.Hour)
	_, _ = w.sched.Run(context.Background())
	w.drain()
	if got := w.product(p.ID); got.Status != domain.StatusActive || got.ConsecutiveFailures != 0 {
		t.Fatalf("not recovered: %s / %d", got.Status, got.ConsecutiveFailures)
	}
}

func TestLoopRemovedProductBecomesGoneAndUnscheduled(t *testing.T) {
	w := newWorld(t)
	p := w.track("discontinued-mixer", "100.00")
	w.drain()
	for i := 0; i < 2; i++ {
		w.clk.Advance(25 * time.Hour)
		_, _ = w.sched.Run(context.Background())
		w.drain()
	}
	got := w.product(p.ID)
	if got.Status != domain.StatusGone || got.NextCheckAt != nil {
		t.Fatalf("status %s next %v", got.Status, got.NextCheckAt)
	}
	w.clk.Advance(30 * 24 * time.Hour)
	if st, _ := w.sched.Run(context.Background()); st.Found != 0 {
		t.Fatal("GONE product still scheduled")
	}
}

func TestLoopCrashedWorkerIsRecoveredByRedelivery(t *testing.T) {
	w := newWorld(t)
	p := w.track("air-fryer", "50.00")
	var m domain.CheckMessage
	w.q.Consume(ctxWithTimeout(t, 300*time.Millisecond), queue.ConsumeOptions{Workers: 1}, func(_ context.Context, msg domain.CheckMessage) error {
		m = msg
		return nil
	})
	// A worker starts the check, then dies before committing.
	if begun, err := w.store.BeginCheck(context.Background(), m, w.clk.Now(), 2*time.Minute); err != nil || !begun {
		t.Fatalf("begin: %v %v", begun, err)
	}
	// The queue redelivers after the visibility timeout (> StaleAfter).
	w.clk.Advance(3 * time.Minute)
	_ = w.q.Enqueue(context.Background(), m)
	w.drain()
	if got := w.product(p.ID); got.Current == nil || got.LastCheckedAt == nil {
		t.Fatalf("crashed check never completed: %+v", got)
	}
	chk, _ := w.store.GetCheck(context.Background(), p.ID, m.CheckID)
	if chk.Status != domain.CheckSucceeded {
		t.Fatalf("check status %s", chk.Status)
	}
}

func TestLoopTransientFailureBacksOff(t *testing.T) {
	w := newWorld(t)
	p := w.track("air-fryer", "50.00")
	w.override("air-fryer", `{"fault":"error"}`)
	w.drain()
	got := w.product(p.ID)
	if got.LastErrorCode != domain.OutcomeFetchTransient || got.Status != domain.StatusActive {
		t.Fatalf("product = %+v", got)
	}
	if delay := got.NextCheckAt.Sub(w.clk.Now()); delay < 15*time.Minute || delay > 17*time.Minute {
		t.Fatalf("first backoff = %v, want ~15m", delay)
	}
}

func mustFind(t *testing.T, slug string) demostore.Product {
	p, ok := demostore.Find(slug)
	if !ok {
		t.Fatalf("no demo product %s", slug)
	}
	return p
}

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
