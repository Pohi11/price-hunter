// Package lambdax adapts Price Hunter's services to Lambda event shapes.
package lambdax

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"golang.org/x/sync/errgroup"

	"github.com/Pohi11/price-hunter/internal/queue"
)

// VisibilityChanger is the SQS call used to back off failed messages.
type VisibilityChanger interface {
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// SQSBatchOptions tunes batch processing.
type SQSBatchOptions struct {
	Concurrency int           // messages processed in parallel within one invocation
	Reserve     time.Duration // stop starting new messages this long before the Lambda deadline
	Log         *slog.Logger
	Now         func() time.Time
	// Visibility, if set, gives failed messages an exponentially growing
	// visibility timeout (queue.RedeliveryBackoff) instead of the queue's
	// fixed one, so retries of infrastructure failures spread out.
	Visibility VisibilityChanger
}

// SQSBatchHandler processes an SQS batch concurrently and reports partial
// failures (ReportBatchItemFailures), so one bad message never forces the
// whole batch to be redelivered.
//
//   - success: the message is deleted by Lambda
//   - handler error: reported as failed -> redelivered after the visibility
//     timeout -> DLQ after maxReceiveCount
//   - malformed body: logged and dropped (redelivery can't fix it)
//   - not started before the deadline cutoff: reported as failed so it is
//     redelivered rather than cut off mid-check
func SQSBatchHandler(handle queue.Handler, opts SQSBatchOptions) func(context.Context, events.SQSEvent) (events.SQSEventResponse, error) {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return func(ctx context.Context, ev events.SQSEvent) (events.SQSEventResponse, error) {
		var (
			mu     sync.Mutex
			failed []events.SQSBatchItemFailure
		)
		fail := func(id string) {
			mu.Lock()
			failed = append(failed, events.SQSBatchItemFailure{ItemIdentifier: id})
			mu.Unlock()
		}
		deadline, hasDeadline := ctx.Deadline()

		g := errgroup.Group{}
		g.SetLimit(opts.Concurrency)
		for _, rec := range ev.Records {
			if hasDeadline && opts.Now().Add(opts.Reserve).After(deadline) {
				fail(rec.MessageId) // not enough time left: hand back unstarted
				continue
			}
			g.Go(func() error {
				msg, err := queue.Decode(rec.Body)
				if err != nil {
					opts.Log.ErrorContext(ctx, "dropping malformed message", "message_id", rec.MessageId, "err", err)
					return nil
				}
				if err := handle(ctx, msg); err != nil {
					receives, _ := strconv.Atoi(rec.Attributes["ApproximateReceiveCount"])
					wait := queue.RedeliveryBackoff(receives)
					opts.Log.WarnContext(ctx, "check failed; message will be retried", "message_id", rec.MessageId,
						"check_id", msg.CheckID, "receive_count", receives, "retry_in", wait.String(), "err", err)
					if opts.Visibility != nil {
						if url, ok := QueueURLFromARN(rec.EventSourceARN); ok {
							_, verr := opts.Visibility.ChangeMessageVisibility(context.WithoutCancel(ctx), &sqs.ChangeMessageVisibilityInput{
								QueueUrl: &url, ReceiptHandle: &rec.ReceiptHandle, VisibilityTimeout: int32(wait / time.Second), //nolint:gosec // <= 900
							})
							if verr != nil {
								opts.Log.WarnContext(ctx, "change visibility failed; queue default applies", "err", verr)
							}
						}
					}
					fail(rec.MessageId)
				}
				return nil
			})
		}
		_ = g.Wait()
		return events.SQSEventResponse{BatchItemFailures: failed}, nil
	}
}

// QueueURLFromARN converts arn:aws:sqs:<region>:<account>:<name> to the
// queue URL SQS APIs expect.
func QueueURLFromARN(arn string) (string, bool) {
	parts := strings.Split(arn, ":")
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sqs" {
		return "", false
	}
	host := "sqs." + parts[3] + ".amazonaws.com"
	if strings.HasPrefix(parts[1], "aws-cn") {
		host += ".cn"
	}
	return "https://" + host + "/" + parts[4] + "/" + parts[5], true
}
