// Package queue carries CheckMessages from the API and scheduler to workers.
// SQS is used in AWS; Mem is an in-process stand-in for local development.
package queue

import (
	"context"
	"errors"
	"sync"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// ErrFull is returned when the in-memory queue's buffer is full.
var ErrFull = errors.New("queue full")

// Mem is a bounded in-process queue. Like SQS it is at-least-once: a
// message whose handler fails is redelivered (up to MaxReceives), so
// handlers must be idempotent either way.
type Mem struct {
	ch          chan memMsg
	maxReceives int

	mu   sync.Mutex
	dead []domain.CheckMessage // messages that exhausted retries, like a DLQ
}

type memMsg struct {
	msg      domain.CheckMessage
	receives int
}

// NewMem returns a queue holding up to size messages.
func NewMem(size, maxReceives int) *Mem {
	if maxReceives <= 0 {
		maxReceives = 5
	}
	return &Mem{ch: make(chan memMsg, size), maxReceives: maxReceives}
}

// Enqueue adds messages without blocking.
func (q *Mem) Enqueue(_ context.Context, msgs ...domain.CheckMessage) error {
	for _, m := range msgs {
		select {
		case q.ch <- memMsg{msg: m}:
		default:
			return ErrFull
		}
	}
	return nil
}

// Depth reports queued messages (used for scheduler backpressure).
func (q *Mem) Depth(context.Context) (int, error) { return len(q.ch), nil }

// Dead returns messages that exceeded MaxReceives.
func (q *Mem) Dead() []domain.CheckMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]domain.CheckMessage(nil), q.dead...)
}

func (q *Mem) requeue(m memMsg) {
	if m.receives >= q.maxReceives {
		q.mu.Lock()
		q.dead = append(q.dead, m.msg)
		q.mu.Unlock()
		return
	}
	select {
	case q.ch <- m:
	default:
		q.mu.Lock()
		q.dead = append(q.dead, m.msg)
		q.mu.Unlock()
	}
}
