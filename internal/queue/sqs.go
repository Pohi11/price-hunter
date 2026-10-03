package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// SQSAPI is the subset of the SQS client used.
type SQSAPI interface {
	SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, opts ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// SQS sends check messages to an SQS queue.
type SQS struct {
	Client   SQSAPI
	QueueURL string
}

// Enqueue sends messages in batches of 10. Partial batch failures are
// reported as an error naming the failed check IDs.
func (q *SQS) Enqueue(ctx context.Context, msgs ...domain.CheckMessage) error {
	for start := 0; start < len(msgs); start += 10 {
		batch := msgs[start:min(start+10, len(msgs))]
		entries := make([]types.SendMessageBatchRequestEntry, len(batch))
		for i, m := range batch {
			body, err := json.Marshal(m)
			if err != nil {
				return err
			}
			entries[i] = types.SendMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), MessageBody: aws.String(string(body))}
		}
		out, err := q.Client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: &q.QueueURL, Entries: entries})
		if err != nil {
			return fmt.Errorf("sqs send batch: %w", err)
		}
		if len(out.Failed) > 0 {
			ids := make([]string, 0, len(out.Failed))
			for _, f := range out.Failed {
				i, _ := strconv.Atoi(aws.ToString(f.Id))
				ids = append(ids, batch[i].CheckID+"("+aws.ToString(f.Code)+")")
			}
			return fmt.Errorf("sqs send batch: %d of %d failed: %v", len(out.Failed), len(batch), ids)
		}
	}
	return nil
}

// Depth returns the approximate number of visible messages (backpressure).
func (q *SQS) Depth(ctx context.Context) (int, error) {
	out, err := q.Client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: &q.QueueURL, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		return 0, fmt.Errorf("sqs depth: %w", err)
	}
	return strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)])
}

// Decode parses a message body.
func Decode(body string) (domain.CheckMessage, error) {
	var m domain.CheckMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return m, fmt.Errorf("decode check message: %w", err)
	}
	if m.ProductID == "" || m.UserID == "" || m.CheckID == "" {
		return m, fmt.Errorf("decode check message: missing ids")
	}
	return m, nil
}
