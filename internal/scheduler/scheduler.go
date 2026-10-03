// Package scheduler finds products whose next_check_at has passed, leases
// them, and enqueues check messages. It runs every few minutes (EventBridge
// Scheduler in AWS, a ticker in server mode) and is safe to run
// concurrently: leases are conditional writes (ADR-003).
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/store"
)

// Store is the persistence the scheduler needs.
type Store interface {
	QueryDue(ctx context.Context, shard int, now time.Time, limit int) ([]store.DueItem, error)
	LeaseDue(ctx context.Context, it store.DueItem, until time.Time) (bool, error)
}

// Queue receives check messages.
type Queue interface {
	Enqueue(ctx context.Context, msgs ...domain.CheckMessage) error
}

// DepthReader reports approximate queue depth for backpressure. Optional.
type DepthReader interface {
	Depth(ctx context.Context) (int, error)
}

// Observer receives per-run statistics (metrics).
type Observer interface {
	SchedulerRun(ctx context.Context, s Stats)
}

// Config tunes a run.
type Config struct {
	Lease         time.Duration // how long a leased product waits before it can be re-picked
	MaxPerRun     int           // cap on products enqueued per run
	MaxQueueDepth int           // skip the run when the queue is deeper than this (0 = no check)
	Shards        int
}

// Stats describes one run.
type Stats struct {
	Found, Leased, LostLeases, Enqueued, EnqueueFailed int
	MaxLag                                             time.Duration   // oldest due item: now - next_check_at
	Lags                                               []time.Duration // per leased product, for the schedule-lag SLI
	QueueDepth                                         int
	SkippedBackpressure                                bool
	Duration                                           time.Duration
}

// Scheduler enqueues due checks.
type Scheduler struct {
	store    Store
	queue    Queue
	depth    DepthReader
	observer Observer
	cfg      Config
	log      *slog.Logger
	now      func() time.Time
	newID    func() string
}

// New builds a Scheduler. depth and observer may be nil.
func New(s Store, q Queue, depth DepthReader, observer Observer, cfg Config, log *slog.Logger) *Scheduler {
	if cfg.Lease == 0 {
		cfg.Lease = 15 * time.Minute
	}
	if cfg.MaxPerRun == 0 {
		cfg.MaxPerRun = 500
	}
	if cfg.Shards == 0 {
		cfg.Shards = store.DueShards
	}
	return &Scheduler{store: s, queue: q, depth: depth, observer: observer, cfg: cfg, log: log,
		now: func() time.Time { return time.Now().UTC() }, newID: func() string { return ulid.Make().String() }}
}

// SetClock replaces the clock (tests).
func (s *Scheduler) SetClock(now func() time.Time) { s.now = now }

// Run performs one scheduling pass.
func (s *Scheduler) Run(ctx context.Context) (Stats, error) {
	start := s.now()
	var st Stats
	defer func() {
		st.Duration = s.now().Sub(start)
		if s.observer != nil {
			s.observer.SchedulerRun(ctx, st)
		}
	}()

	if s.depth != nil && s.cfg.MaxQueueDepth > 0 {
		d, err := s.depth.Depth(ctx)
		if err != nil {
			s.log.WarnContext(ctx, "queue depth unavailable; continuing", "err", err)
		} else {
			st.QueueDepth = d
			if d > s.cfg.MaxQueueDepth {
				// Backpressure: let workers catch up. Due products simply wait
				// in DynamoDB; schedule lag rises and alarms if it persists.
				st.SkippedBackpressure = true
				s.log.WarnContext(ctx, "scheduler skipped: queue too deep", "depth", d, "max", s.cfg.MaxQueueDepth)
				return st, nil
			}
		}
	}

	var errs []error
	remaining := s.cfg.MaxPerRun
	for shard := 0; shard < s.cfg.Shards && remaining > 0; shard++ {
		due, err := s.store.QueryDue(ctx, shard, start, remaining)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		st.Found += len(due)
		msgs := make([]domain.CheckMessage, 0, len(due))
		for _, it := range due {
			won, err := s.store.LeaseDue(ctx, it, start.Add(s.cfg.Lease))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !won {
				st.LostLeases++ // another scheduler run took it
				continue
			}
			st.Leased++
			lag := max(start.Sub(it.DueAt), 0)
			st.Lags = append(st.Lags, lag)
			if lag > st.MaxLag {
				st.MaxLag = lag
			}
			msgs = append(msgs, domain.CheckMessage{UserID: it.UserID, ProductID: it.ProductID, CheckID: s.newID(),
				Trigger: domain.TriggerScheduled, EnqueuedAt: start})
		}
		remaining -= len(msgs)
		for i := 0; i < len(msgs); i += 10 { // SQS SendMessageBatch limit
			batch := msgs[i:min(i+10, len(msgs))]
			if err := s.queue.Enqueue(ctx, batch...); err != nil {
				// Not retried here: the lease expires and the next run re-picks them.
				st.EnqueueFailed += len(batch)
				errs = append(errs, err)
				continue
			}
			st.Enqueued += len(batch)
		}
	}

	attrs := []any{"found", st.Found, "enqueued", st.Enqueued, "lost_leases", st.LostLeases,
		"enqueue_failed", st.EnqueueFailed, "max_lag_s", int(st.MaxLag.Seconds())}
	if len(errs) > 0 {
		s.log.ErrorContext(ctx, "scheduler run had errors", append(attrs, "err", errors.Join(errs...))...)
	} else if st.Found > 0 {
		s.log.InfoContext(ctx, "scheduler run", attrs...)
	}
	return st, errors.Join(errs...)
}
