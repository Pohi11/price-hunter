package lambdax

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Pohi11/price-hunter/internal/domain"
)

func record(id, checkID string) events.SQSMessage {
	body, _ := json.Marshal(domain.CheckMessage{UserID: "u1", ProductID: "P1", CheckID: checkID, Trigger: domain.TriggerScheduled})
	return events.SQSMessage{MessageId: id, Body: string(body), Attributes: map[string]string{"ApproximateReceiveCount": "1"}}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func failedIDs(r events.SQSEventResponse) []string {
	var ids []string
	for _, f := range r.BatchItemFailures {
		ids = append(ids, f.ItemIdentifier)
	}
	sort.Strings(ids)
	return ids
}

func TestPartialBatchFailures(t *testing.T) {
	h := SQSBatchHandler(func(_ context.Context, m domain.CheckMessage) error {
		if m.CheckID == "bad" {
			return errors.New("dynamodb unavailable")
		}
		return nil
	}, SQSBatchOptions{Concurrency: 3, Log: quiet})

	ev := events.SQSEvent{Records: []events.SQSMessage{
		record("m1", "ok1"), record("m2", "bad"), record("m3", "ok2"),
		{MessageId: "m4", Body: "{not json"}, // malformed: dropped, not retried forever
	}}
	resp, err := h(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if got := failedIDs(resp); len(got) != 1 || got[0] != "m2" {
		t.Fatalf("failures = %v, want [m2]", got)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	var inflight, peak atomic.Int32
	h := SQSBatchHandler(func(context.Context, domain.CheckMessage) error {
		n := inflight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
		return nil
	}, SQSBatchOptions{Concurrency: 2, Log: quiet})
	var recs []events.SQSMessage
	for i := 0; i < 10; i++ {
		recs = append(recs, record(string(rune('a'+i)), "c"))
	}
	if _, err := h(context.Background(), events.SQSEvent{Records: recs}); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d > 2", peak.Load())
	}
}

func TestDeadlineCutoffReturnsUnstartedMessages(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var started atomic.Int32
	h := SQSBatchHandler(func(context.Context, domain.CheckMessage) error {
		started.Add(1)
		return nil
	}, SQSBatchOptions{Concurrency: 1, Reserve: 30 * time.Second, Log: quiet, Now: func() time.Time { return now }})

	// Only 10s left in the invocation: nothing may start.
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Second))
	defer cancel()
	resp, _ := h(ctx, events.SQSEvent{Records: []events.SQSMessage{record("m1", "c1"), record("m2", "c2")}})
	if started.Load() != 0 || len(resp.BatchItemFailures) != 2 {
		t.Fatalf("started=%d failures=%v", started.Load(), failedIDs(resp))
	}
}

func TestRecordedSQSEventShape(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/events/sqs-check.json")
	if err != nil {
		t.Fatal(err)
	}
	var ev events.SQSEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	var got domain.CheckMessage
	h := SQSBatchHandler(func(_ context.Context, m domain.CheckMessage) error { got = m; return nil }, SQSBatchOptions{Log: quiet})
	resp, _ := h(context.Background(), ev)
	if len(resp.BatchItemFailures) != 0 || got.CheckID != "01JA9X2T7B" || got.Trigger != domain.TriggerScheduled {
		t.Fatalf("decoded %+v, failures %v", got, resp.BatchItemFailures)
	}
}

type fakeVisibility struct{ calls []int32 }

func (f *fakeVisibility) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.calls = append(f.calls, in.VisibilityTimeout)
	if *in.QueueUrl != "https://sqs.us-east-1.amazonaws.com/123456789012/pricehunter-dev-checks" {
		return nil, errors.New("wrong queue url " + *in.QueueUrl)
	}
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func TestFailedMessagesBackOffExponentially(t *testing.T) {
	vis := &fakeVisibility{}
	h := SQSBatchHandler(func(context.Context, domain.CheckMessage) error { return errors.New("throttled") },
		SQSBatchOptions{Log: quiet, Visibility: vis})
	var recs []events.SQSMessage
	for i, rc := range []string{"1", "3", "9"} {
		r := record(string(rune('a'+i)), "c")
		r.Attributes["ApproximateReceiveCount"] = rc
		r.EventSourceARN = "arn:aws:sqs:us-east-1:123456789012:pricehunter-dev-checks"
		r.ReceiptHandle = "rh"
		recs = append(recs, r)
	}
	resp, _ := h(context.Background(), events.SQSEvent{Records: recs})
	if len(resp.BatchItemFailures) != 3 {
		t.Fatalf("failures = %d", len(resp.BatchItemFailures))
	}
	sort.Slice(vis.calls, func(i, j int) bool { return vis.calls[i] < vis.calls[j] })
	if len(vis.calls) != 3 || vis.calls[0] != 30 || vis.calls[1] != 120 || vis.calls[2] != 900 {
		t.Fatalf("visibility timeouts = %v, want [30 120 900]", vis.calls)
	}
}

func TestQueueURLFromARN(t *testing.T) {
	if u, ok := QueueURLFromARN("arn:aws:sqs:eu-west-1:111122223333:q"); !ok || u != "https://sqs.eu-west-1.amazonaws.com/111122223333/q" {
		t.Fatalf("got %q %v", u, ok)
	}
	if _, ok := QueueURLFromARN("not-an-arn"); ok {
		t.Fatal("accepted garbage")
	}
}
