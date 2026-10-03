package lambdax

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// StreamHandlers are the reactions to table changes.
type StreamHandlers struct {
	// NotificationCreated delivers an outbox notification (ADR-004).
	NotificationCreated func(ctx context.Context, userID, notificationID string) error
	// ProductDeleted purges the product's history partition.
	ProductDeleted func(ctx context.Context, productID string) error
	Log            *slog.Logger
}

// StreamHandler processes DynamoDB Streams batches. The event source
// mapping's filter criteria already limit delivery to Notification INSERTs
// and Product REMOVEs; anything else is ignored defensively.
//
// Stream records are ordered per shard, so on the first failure it reports
// that record (and Lambda retries from there); records after it are not
// processed in this invocation. Combined with bisect-on-error and a bounded
// retry count, one bad record can't block a shard forever: it ends up in the
// on-failure destination (streams DLQ).
func StreamHandler(h StreamHandlers) func(context.Context, events.DynamoDBEvent) (events.DynamoDBEventResponse, error) {
	return func(ctx context.Context, ev events.DynamoDBEvent) (events.DynamoDBEventResponse, error) {
		for _, rec := range ev.Records {
			if err := handleRecord(ctx, h, rec); err != nil {
				h.Log.WarnContext(ctx, "stream record failed; will retry", "event", rec.EventName,
					"sequence", rec.Change.SequenceNumber, "err", err)
				return events.DynamoDBEventResponse{BatchItemFailures: []events.DynamoDBBatchItemFailure{
					{ItemIdentifier: rec.Change.SequenceNumber},
				}}, nil
			}
		}
		return events.DynamoDBEventResponse{}, nil
	}
}

func handleRecord(ctx context.Context, h StreamHandlers, rec events.DynamoDBEventRecord) error {
	switch events.DynamoDBOperationType(rec.EventName) {
	case events.DynamoDBOperationTypeInsert:
		img := rec.Change.NewImage
		if str(img, "entity") != "Notification" {
			return nil
		}
		userID := strings.TrimPrefix(str(img, "PK"), "USER#")
		id := str(img, "notification_id")
		if userID == "" || id == "" {
			h.Log.ErrorContext(ctx, "notification record missing keys; skipping", "sequence", rec.Change.SequenceNumber)
			return nil
		}
		return h.NotificationCreated(ctx, userID, id)
	case events.DynamoDBOperationTypeRemove:
		img := rec.Change.OldImage
		if str(img, "entity") != "Product" {
			return nil
		}
		if id := str(img, "product_id"); id != "" {
			return h.ProductDeleted(ctx, id)
		}
	}
	return nil
}

func str(img map[string]events.DynamoDBAttributeValue, key string) string {
	v, ok := img[key]
	if !ok || v.DataType() != events.DataTypeString {
		return ""
	}
	return v.String()
}
