package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// Handler processes one message. A non-nil error means "redeliver later".
type Handler func(ctx context.Context, msg domain.CheckMessage) error

// ConsumeOptions configures a consumer's worker pool.
type ConsumeOptions struct {
	Workers int           // concurrent handlers
	Drain   time.Duration // on shutdown, how long in-flight handlers may keep running
	Log     *slog.Logger
}

// drainContext returns a context for handlers that outlives ctx by d: when
// ctx is cancelled (shutdown), in-flight work gets d to finish before its
// own context is cancelled.
func drainContext(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		t := time.NewTimer(d)
		select {
		case <-t.C:
			cancel()
		case <-hctx.Done():
			t.Stop()
		}
	})
	return hctx, func() { stop(); cancel() }
}

// Consume runs Workers goroutines pulling from the in-memory queue until ctx
// is cancelled, then waits (up to Drain) for in-flight messages.
func (q *Mem) Consume(ctx context.Context, opts ConsumeOptions, handle Handler) {
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	hctx, cancel := drainContext(ctx, opts.Drain)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return // stop taking new work
				case m := <-q.ch:
					m.receives++
					if err := handle(hctx, m.msg); err != nil {
						if opts.Log != nil {
							opts.Log.WarnContext(hctx, "message failed; will redeliver", "check_id", m.msg.CheckID,
								"receives", m.receives, "err", err)
						}
						// Like a visibility timeout: back off before redelivery.
						delay := time.Duration(m.receives) * time.Second
						time.AfterFunc(delay, func() { q.requeue(m) })
					}
				}
			}
		}()
	}
	wg.Wait()
}
