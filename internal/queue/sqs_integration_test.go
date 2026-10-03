//go:build integration

package queue_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/queue"
	"github.com/Pohi11/price-hunter/internal/testutil"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// queuePair creates a fresh queue whose messages move to a DLQ after
// maxReceives, with a short visibility timeout so tests run in seconds.
func queuePair(t *testing.T, maxReceives int) (*sqs.Client, string, string) {
	t.Helper()
	c := testutil.SQSClient(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", rand.IntN(1_000_000))
	dlq, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("t-dlq-" + suffix)})
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := c.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: dlq.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	main, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("t-main-" + suffix), Attributes: map[string]string{
		"VisibilityTimeout": "1",
		"RedrivePolicy":     fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"%d"}`, attrs.Attributes["QueueArn"], maxReceives),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: main.QueueUrl})
		_, _ = c.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl})
	})
	return c, *main.QueueUrl, *dlq.QueueUrl
}

func msgs(n int) []domain.CheckMessage {
	out := make([]domain.CheckMessage, n)
	for i := range out {
		out[i] = domain.CheckMessage{UserID: "u1", ProductID: "P1", CheckID: fmt.Sprintf("C%03d", i), Trigger: domain.TriggerScheduled}
	}
	return out
}

func consumeUntil(t *testing.T, q *queue.SQS, opts queue.SQSConsumeOptions, h queue.Handler, done func() bool, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { q.Consume(ctx, opts, h); close(finished) }()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			cancel()
			<-finished
			t.Fatal("condition not reached before timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-finished
}

func TestSQSEnqueueConsumeAndDelete(t *testing.T) {
	c, url, _ := queuePair(t, 5)
	q := &queue.SQS{Client: c, QueueURL: url}
	if err := q.Enqueue(context.Background(), msgs(25)...); err != nil { // 3 SendMessageBatch calls
		t.Fatal(err)
	}
	if d, _ := q.Depth(context.Background()); d != 25 {
		t.Fatalf("depth = %d, want 25", d)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	consumeUntil(t, q, queue.SQSConsumeOptions{ConsumeOptions: queue.ConsumeOptions{Workers: 4, Log: quiet}, WaitTime: time.Second},
		func(_ context.Context, m domain.CheckMessage) error {
			mu.Lock()
			seen[m.CheckID]++
			mu.Unlock()
			return nil
		},
		func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == 25 }, 20*time.Second)

	time.Sleep(1500 * time.Millisecond) // past the visibility timeout: anything undeleted would reappear
	if d, _ := q.Depth(context.Background()); d != 0 {
		t.Fatalf("depth after consume = %d: messages were not deleted", d)
	}
}

func TestSQSRetryWithBackoffThenSucceed(t *testing.T) {
	c, url, _ := queuePair(t, 5)
	q := &queue.SQS{Client: c, QueueURL: url}
	_ = q.Enqueue(context.Background(), msgs(1)...)
	var attempts atomic.Int32
	var firstAt, secondAt time.Time
	consumeUntil(t, q, queue.SQSConsumeOptions{
		ConsumeOptions: queue.ConsumeOptions{Workers: 1, Log: quiet}, WaitTime: time.Second,
		Backoff: func(int) time.Duration { return 2 * time.Second },
	}, func(context.Context, domain.CheckMessage) error {
		switch attempts.Add(1) {
		case 1:
			firstAt = time.Now()
			return errors.New("dynamodb throttled")
		default:
			secondAt = time.Now()
			return nil
		}
	}, func() bool { return attempts.Load() >= 2 }, 20*time.Second)

	if gap := secondAt.Sub(firstAt); gap < 1800*time.Millisecond {
		t.Fatalf("redelivered after %v; backoff visibility not applied", gap)
	}
}

func TestSQSPoisonMessageLandsInDLQAndRedrives(t *testing.T) {
	c, url, dlqURL := queuePair(t, 2)
	q := &queue.SQS{Client: c, QueueURL: url}
	_ = q.Enqueue(context.Background(), msgs(1)...)

	var attempts atomic.Int32
	consumeUntil(t, q, queue.SQSConsumeOptions{
		ConsumeOptions: queue.ConsumeOptions{Workers: 1, Log: quiet}, WaitTime: time.Second,
		Backoff: func(int) time.Duration { return time.Second },
	}, func(context.Context, domain.CheckMessage) error {
		attempts.Add(1)
		return errors.New("bug: always fails")
	}, func() bool {
		d, _ := (&queue.SQS{Client: c, QueueURL: dlqURL}).Depth(context.Background())
		return d == 1
	}, 30*time.Second)

	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want maxReceiveCount = 2", attempts.Load())
	}
	dl, err := queue.Peek(context.Background(), c, dlqURL, 10)
	if err != nil || len(dl) != 1 || dl[0].Body == "" {
		t.Fatalf("peek: %+v %v", dl, err)
	}
	// After the fix ships, redrive puts it back on the main queue.
	n, err := queue.Redrive(context.Background(), c, dlqURL, url, 10)
	if err != nil || n != 1 {
		t.Fatalf("redrive: %d %v", n, err)
	}
	if left, _ := queue.Peek(context.Background(), c, dlqURL, 10); len(left) != 0 {
		t.Fatalf("DLQ not emptied: %d left", len(left))
	}
	// Written by the consumer goroutine, read by the polling condition.
	var replayed atomic.Value
	consumeUntil(t, q, queue.SQSConsumeOptions{ConsumeOptions: queue.ConsumeOptions{Workers: 1, Log: quiet}, WaitTime: time.Second},
		func(_ context.Context, m domain.CheckMessage) error { replayed.Store(m.CheckID); return nil },
		func() bool { return replayed.Load() != nil }, 15*time.Second)
	if got := replayed.Load(); got != "C000" {
		t.Fatalf("replayed %v", got)
	}
}

func TestSQSMalformedMessageIsDropped(t *testing.T) {
	c, url, dlqURL := queuePair(t, 2)
	_, _ = c.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: &url, MessageBody: aws.String("{not json")})
	q := &queue.SQS{Client: c, QueueURL: url}
	called := atomic.Bool{}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	q.Consume(ctx, queue.SQSConsumeOptions{ConsumeOptions: queue.ConsumeOptions{Workers: 1, Log: quiet}, WaitTime: time.Second},
		func(context.Context, domain.CheckMessage) error { called.Store(true); return nil })
	if called.Load() {
		t.Fatal("handler called with a malformed body")
	}
	if d, _ := (&queue.SQS{Client: c, QueueURL: url}).Depth(context.Background()); d != 0 {
		t.Fatalf("malformed message not deleted (depth %d)", d)
	}
	if dl, _ := queue.Peek(context.Background(), c, dlqURL, 10); len(dl) != 0 {
		t.Fatal("malformed message should be dropped, not dead-lettered repeatedly")
	}
}
