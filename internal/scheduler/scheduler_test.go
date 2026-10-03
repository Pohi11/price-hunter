package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	mu     sync.Mutex
	due    map[int][]store.DueItem
	leased map[string]bool
	steal  map[string]bool // lease already taken by another run
}

func (f *fakeStore) QueryDue(_ context.Context, shard int, _ time.Time, limit int) ([]store.DueItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.DueItem
	for _, it := range f.due[shard] {
		if !f.leased[it.ProductID] && len(out) < limit {
			out = append(out, it)
		}
	}
	return out, nil
}

func (f *fakeStore) LeaseDue(_ context.Context, it store.DueItem, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.steal[it.ProductID] || f.leased[it.ProductID] {
		return false, nil
	}
	f.leased[it.ProductID] = true
	return true, nil
}

type fakeQueue struct {
	msgs    []domain.CheckMessage
	batches []int
	fail    bool
	depth   int
}

func (q *fakeQueue) Enqueue(_ context.Context, msgs ...domain.CheckMessage) error {
	if q.fail {
		return errors.New("sqs unavailable")
	}
	q.batches = append(q.batches, len(msgs))
	q.msgs = append(q.msgs, msgs...)
	return nil
}

func (q *fakeQueue) Depth(context.Context) (int, error) { return q.depth, nil }

func storeWith(n int) *fakeStore {
	fs := &fakeStore{due: map[int][]store.DueItem{}, leased: map[string]bool{}, steal: map[string]bool{}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("P%02d", i)
		shard := i % store.DueShards
		fs.due[shard] = append(fs.due[shard], store.DueItem{UserID: "u1", ProductID: id, DueAt: t0.Add(-time.Duration(i) * time.Minute)})
	}
	return fs
}

func newScheduler(fs *fakeStore, q *fakeQueue, cfg Config) *Scheduler {
	s := New(fs, q, q, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetClock(func() time.Time { return t0 })
	return s
}

func TestRunLeasesAndEnqueuesInBatches(t *testing.T) {
	fs := storeWith(25)
	q := &fakeQueue{}
	st, err := newScheduler(fs, q, Config{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Found != 25 || st.Leased != 25 || st.Enqueued != 25 || len(q.msgs) != 25 {
		t.Fatalf("stats = %+v", st)
	}
	for _, b := range q.batches {
		if b > 10 {
			t.Fatalf("batch of %d exceeds SQS limit", b)
		}
	}
	if st.MaxLag != 24*time.Minute {
		t.Fatalf("max lag = %v", st.MaxLag)
	}
	ids := map[string]bool{}
	for _, m := range q.msgs {
		if m.Trigger != domain.TriggerScheduled || ids[m.CheckID] {
			t.Fatalf("bad message %+v", m)
		}
		ids[m.CheckID] = true
	}
	// A second run finds nothing: everything is leased.
	st2, _ := newScheduler(fs, q, Config{}).Run(context.Background())
	if st2.Found != 0 {
		t.Fatalf("re-picked leased products: %+v", st2)
	}
}

func TestRunRespectsMaxPerRun(t *testing.T) {
	fs := storeWith(30)
	q := &fakeQueue{}
	st, _ := newScheduler(fs, q, Config{MaxPerRun: 12}).Run(context.Background())
	if st.Enqueued != 12 {
		t.Fatalf("enqueued %d, want 12", st.Enqueued)
	}
}

func TestLostLeasesAreSkipped(t *testing.T) {
	fs := storeWith(4)
	fs.steal["P01"] = true
	q := &fakeQueue{}
	st, _ := newScheduler(fs, q, Config{}).Run(context.Background())
	if st.LostLeases != 1 || st.Enqueued != 3 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestBackpressureSkipsRun(t *testing.T) {
	fs := storeWith(5)
	q := &fakeQueue{depth: 5000}
	st, err := newScheduler(fs, q, Config{MaxQueueDepth: 1000}).Run(context.Background())
	if err != nil || !st.SkippedBackpressure || st.Enqueued != 0 || len(fs.leased) != 0 {
		t.Fatalf("backpressure not applied: %+v %v", st, err)
	}
}

func TestEnqueueFailureIsReportedAndLeaseRecovers(t *testing.T) {
	fs := storeWith(3)
	q := &fakeQueue{fail: true}
	st, err := newScheduler(fs, q, Config{}).Run(context.Background())
	if err == nil || st.EnqueueFailed != 3 {
		t.Fatalf("stats = %+v err = %v", st, err)
	}
	// The leases stand; products become due again when they expire, so
	// nothing is lost without any retry bookkeeping here.
	if len(fs.leased) != 3 {
		t.Fatal("lease should remain so the product is retried after expiry")
	}
}
