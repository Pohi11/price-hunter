package queue

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// DLQAPI is the SQS surface used by DLQ tooling.
type DLQAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
	DeleteMessageBatch(ctx context.Context, in *sqs.DeleteMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
	ChangeMessageVisibilityBatch(ctx context.Context, in *sqs.ChangeMessageVisibilityBatchInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error)
}

// DeadLetter is one message sitting in a DLQ.
type DeadLetter struct {
	MessageID string
	Body      string
	Receives  int
}

// Peek returns up to max messages without removing them. Each received
// batch is explicitly released (visibility 0) so this is safe to run any
// time; not every SQS implementation honors VisibilityTimeout=0 on receive.
func Peek(ctx context.Context, c DLQAPI, dlqURL string, max int) ([]DeadLetter, error) {
	var out []DeadLetter
	seen := map[string]bool{}
	for len(out) < max {
		res, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: &dlqURL, MaxNumberOfMessages: int32(min(10, max-len(out))), //nolint:gosec // <= 10
			VisibilityTimeout: 0, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			return out, fmt.Errorf("peek: %w", err)
		}
		release := make([]types.ChangeMessageVisibilityBatchRequestEntry, 0, len(res.Messages))
		fresh := 0
		for i, m := range res.Messages {
			release = append(release, types.ChangeMessageVisibilityBatchRequestEntry{
				Id: aws.String(strconv.Itoa(i)), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 0})
			id := aws.ToString(m.MessageId)
			if seen[id] {
				continue
			}
			seen[id], fresh = true, fresh+1
			n, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
			out = append(out, DeadLetter{MessageID: id, Body: aws.ToString(m.Body), Receives: n})
		}
		if len(release) > 0 {
			if _, err := c.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{QueueUrl: &dlqURL, Entries: release}); err != nil {
				return out, fmt.Errorf("peek release: %w", err)
			}
		}
		if fresh == 0 {
			break
		}
	}
	return out, nil
}

// Redrive moves up to max messages from dlqURL back to targetURL. Each batch
// is sent before it is deleted, so a crash mid-redrive can duplicate a
// message (harmless: consumers are idempotent) but never lose one.
func Redrive(ctx context.Context, c DLQAPI, dlqURL, targetURL string, max int) (int, error) {
	moved := 0
	for moved < max {
		res, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: &dlqURL, MaxNumberOfMessages: int32(min(10, max-moved)), //nolint:gosec // <= 10
			VisibilityTimeout: 60, WaitTimeSeconds: 1,
		})
		if err != nil {
			return moved, fmt.Errorf("redrive receive: %w", err)
		}
		if len(res.Messages) == 0 {
			return moved, nil
		}
		send := make([]types.SendMessageBatchRequestEntry, len(res.Messages))
		del := make([]types.DeleteMessageBatchRequestEntry, 0, len(res.Messages))
		for i, m := range res.Messages {
			send[i] = types.SendMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), MessageBody: m.Body}
		}
		sent, err := c.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: &targetURL, Entries: send})
		if err != nil {
			return moved, fmt.Errorf("redrive send: %w", err)
		}
		for _, ok := range sent.Successful {
			i, _ := strconv.Atoi(aws.ToString(ok.Id))
			del = append(del, types.DeleteMessageBatchRequestEntry{Id: ok.Id, ReceiptHandle: res.Messages[i].ReceiptHandle})
		}
		if len(del) > 0 {
			if _, err := c.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: &dlqURL, Entries: del}); err != nil {
				return moved, fmt.Errorf("redrive delete: %w", err)
			}
		}
		moved += len(del)
		if len(sent.Failed) > 0 {
			return moved, fmt.Errorf("redrive: %d messages failed to send; they remain in the DLQ", len(sent.Failed))
		}
	}
	return moved, nil
}
