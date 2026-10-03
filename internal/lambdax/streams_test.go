package lambdax

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func loadStream(t *testing.T) events.DynamoDBEvent {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/events/dynamodb-stream.json")
	if err != nil {
		t.Fatal(err)
	}
	var ev events.DynamoDBEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestStreamRoutesRecords(t *testing.T) {
	var notified, purged []string
	h := StreamHandler(StreamHandlers{
		NotificationCreated: func(_ context.Context, u, id string) error { notified = append(notified, u+"/"+id); return nil },
		ProductDeleted:      func(_ context.Context, id string) error { purged = append(purged, id); return nil },
		Log:                 quiet,
	})
	resp, err := h(context.Background(), loadStream(t))
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	// INSERT Notification -> dispatch; MODIFY Product and TTL REMOVE of a Check -> ignored.
	if len(notified) != 1 || notified[0] != "a1b2/01JA9Z0000" {
		t.Fatalf("notified = %v", notified)
	}
	if len(purged) != 1 || purged[0] != "01JA8Q9K3Z" {
		t.Fatalf("purged = %v", purged)
	}
}

func TestStreamReportsFirstFailureAndStops(t *testing.T) {
	var purged int
	h := StreamHandler(StreamHandlers{
		NotificationCreated: func(context.Context, string, string) error { return errors.New("ses throttled") },
		ProductDeleted:      func(context.Context, string) error { purged++; return nil },
		Log:                 quiet,
	})
	resp, _ := h(context.Background(), loadStream(t))
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "111" {
		t.Fatalf("failures = %+v", resp.BatchItemFailures)
	}
	// Ordered processing: later records wait for the retry.
	if purged != 0 {
		t.Fatal("records after a failure must not be processed in this invocation")
	}
}
