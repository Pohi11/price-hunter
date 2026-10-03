package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

func TestMemConsumeConcurrencyBound(t *testing.T) {
	q := NewMem(100, 3)
	for i := 0; i < 20; i++ {
		_ = q.Enqueue(context.Background(), domain.CheckMessage{CheckID: string(rune('a' + i))})
	}
	var inflight, peak, done atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for done.Load() < 20 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	q.Consume(ctx, ConsumeOptions{Workers: 4, Drain: time.Second}, func(context.Context, domain.CheckMessage) error {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inflight.Add(-1)
		done.Add(1)
		return nil
	})
	if peak.Load() > 4 {
		t.Fatalf("peak concurrency %d exceeds 4 workers", peak.Load())
	}
	if done.Load() != 20 {
		t.Fatalf("processed %d of 20", done.Load())
	}
}

func TestMemRedeliversThenDeadLetters(t *testing.T) {
	q := NewMem(10, 2)
	_ = q.Enqueue(context.Background(), domain.CheckMessage{CheckID: "poison"})
	var attempts atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() {
		for len(q.Dead()) == 0 && ctx.Err() == nil {
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	q.Consume(ctx, ConsumeOptions{Workers: 1}, func(context.Context, domain.CheckMessage) error {
		attempts.Add(1)
		return errors.New("bug")
	})
	if attempts.Load() != 2 || len(q.Dead()) != 1 || q.Dead()[0].CheckID != "poison" {
		t.Fatalf("attempts=%d dead=%v", attempts.Load(), q.Dead())
	}
}

func TestMemDrainLetsInFlightFinish(t *testing.T) {
	q := NewMem(10, 3)
	_ = q.Enqueue(context.Background(), domain.CheckMessage{CheckID: "slow"})
	started := make(chan struct{})
	var finished atomic.Bool
	var handlerCtxErr error
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }() // shutdown while the handler runs

	q.Consume(ctx, ConsumeOptions{Workers: 1, Drain: 2 * time.Second}, func(hctx context.Context, _ domain.CheckMessage) error {
		close(started)
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		handlerCtxErr = hctx.Err()
		mu.Unlock()
		finished.Store(true)
		return nil
	})
	if !finished.Load() || handlerCtxErr != nil {
		t.Fatalf("in-flight work was cut off (finished=%v ctxErr=%v)", finished.Load(), handlerCtxErr)
	}
}

func TestMemDrainDeadlineCancelsStuckWork(t *testing.T) {
	q := NewMem(10, 3)
	_ = q.Enqueue(context.Background(), domain.CheckMessage{CheckID: "stuck"})
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() { <-started; cancel() }()
	start := time.Now()
	q.Consume(ctx, ConsumeOptions{Workers: 1, Drain: 100 * time.Millisecond}, func(hctx context.Context, _ domain.CheckMessage) error {
		close(started)
		<-hctx.Done() // a handler that only stops when told to
		return hctx.Err()
	})
	if time.Since(start) > 2*time.Second {
		t.Fatal("drain deadline not enforced")
	}
}

func TestMemFull(t *testing.T) {
	q := NewMem(1, 1)
	_ = q.Enqueue(context.Background(), domain.CheckMessage{})
	if err := q.Enqueue(context.Background(), domain.CheckMessage{}); !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v", err)
	}
	if d, _ := q.Depth(context.Background()); d != 1 {
		t.Fatalf("depth = %d", d)
	}
}
