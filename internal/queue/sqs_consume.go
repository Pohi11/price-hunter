package queue

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// RedeliveryBackoff returns how long a failed message stays invisible
// before its next delivery: 30s, 60s, 120s, ... capped at 15 minutes. SQS
// by itself would redeliver after the fixed visibility timeout; spacing out
// retries gives a struggling dependency (e.g. throttled DynamoDB) room to
// recover before the message exhausts maxReceiveCount and lands in the DLQ.
func RedeliveryBackoff(receiveCount int) time.Duration {
	if receiveCount < 1 {
		receiveCount = 1
	}
	d := 30 * time.Second << min(receiveCount-1, 10)
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

// SQSConsumeOptions configures the server-mode SQS consumer.
type SQSConsumeOptions struct {
	ConsumeOptions
	WaitTime    time.Duration                    // long-poll wait (max 20s)
	Backoff     func(receives int) time.Duration // visibility after a failure; nil = RedeliveryBackoff
	MaxMessages int32                            // per receive call (max 10)
}

// Consume long-polls SQS and processes messages with a bounded worker pool
// until ctx is cancelled, then drains in-flight work. Successful messages
// are deleted; failed ones get an exponentially growing visibility timeout.
// Nothing else is needed for crash safety: an unacknowledged message simply
// reappears when its visibility timeout expires.
func (q *SQS) Consume(ctx context.Context, opts SQSConsumeOptions, handle Handler) {
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	if opts.WaitTime == 0 {
		opts.WaitTime = 20 * time.Second
	}
	if opts.MaxMessages == 0 {
		opts.MaxMessages = 10
	}
	if opts.Backoff == nil {
		opts.Backoff = RedeliveryBackoff
	}
	hctx, cancel := drainContext(ctx, opts.Drain)
	defer cancel()

	work := make(chan types.Message)
	var wg sync.WaitGroup
	for i := 0; i < opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range work {
				q.process(hctx, opts, handle, m)
			}
		}()
	}

	for ctx.Err() == nil {
		out, err := q.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:                    &q.QueueURL,
			MaxNumberOfMessages:         opts.MaxMessages,
			WaitTimeSeconds:             int32(opts.WaitTime / time.Second), //nolint:gosec // <= 20
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if opts.Log != nil {
				opts.Log.WarnContext(ctx, "sqs receive failed; retrying", "err", err)
			}
			select { // brief pause so an outage doesn't spin
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, m := range out.Messages {
			select {
			case work <- m: // blocks while all workers are busy: natural backpressure
			case <-ctx.Done():
				// Not started: leave it; it becomes visible again on its own.
			}
		}
	}
	close(work)
	wg.Wait()
}

func (q *SQS) process(ctx context.Context, opts SQSConsumeOptions, handle Handler, m types.Message) {
	receives, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	msg, err := Decode(aws.ToString(m.Body))
	if err != nil {
		if opts.Log != nil {
			opts.Log.ErrorContext(ctx, "dropping malformed message", "message_id", aws.ToString(m.MessageId), "err", err)
		}
		q.delete(ctx, opts, m)
		return
	}
	if err := handle(ctx, msg); err != nil {
		wait := opts.Backoff(receives)
		if opts.Log != nil {
			opts.Log.WarnContext(ctx, "check failed; backing off before redelivery", "check_id", msg.CheckID,
				"receives", receives, "retry_in", wait.String(), "err", err)
		}
		_, verr := q.Client.ChangeMessageVisibility(context.WithoutCancel(ctx), &sqs.ChangeMessageVisibilityInput{
			QueueUrl: &q.QueueURL, ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: int32(wait / time.Second), //nolint:gosec // <= 900
		})
		if verr != nil && opts.Log != nil {
			opts.Log.WarnContext(ctx, "change visibility failed; default timeout applies", "err", verr)
		}
		return
	}
	q.delete(ctx, opts, m)
}

func (q *SQS) delete(ctx context.Context, opts SQSConsumeOptions, m types.Message) {
	// Use a non-cancelled context: the work is done; failing to ack would
	// only cause a harmless (idempotent) duplicate, but avoid it if we can.
	_, err := q.Client.DeleteMessage(context.WithoutCancel(ctx), &sqs.DeleteMessageInput{QueueUrl: &q.QueueURL, ReceiptHandle: m.ReceiptHandle})
	if err != nil && opts.Log != nil {
		opts.Log.WarnContext(ctx, "delete failed; message will be redelivered (idempotent)", "err", err)
	}
}
