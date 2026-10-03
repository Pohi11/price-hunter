//go:build integration

package e2e_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"
	sttypes "github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/lambdax"
	"github.com/Pohi11/price-hunter/internal/notify"
	"github.com/Pohi11/price-hunter/internal/store"
	"github.com/Pohi11/price-hunter/internal/testutil"
)

// readStream returns every record currently in the table's stream, in the
// shape Lambda delivers to the streams function.
func readStream(t *testing.T, db *dynamodb.Client, table string) []events.DynamoDBEventRecord {
	t.Helper()
	ctx := context.Background()
	desc, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &table})
	if err != nil {
		t.Fatal(err)
	}
	sc := dynamodbstreams.NewFromConfig(testutil.LocalAWSConfig(), func(o *dynamodbstreams.Options) {
		o.BaseEndpoint = aws.String(testutil.DynamoEndpoint())
	})
	stream, err := sc.DescribeStream(ctx, &dynamodbstreams.DescribeStreamInput{StreamArn: desc.Table.LatestStreamArn})
	if err != nil {
		t.Fatal(err)
	}
	var out []events.DynamoDBEventRecord
	for _, sh := range stream.StreamDescription.Shards {
		it, err := sc.GetShardIterator(ctx, &dynamodbstreams.GetShardIteratorInput{
			StreamArn: desc.Table.LatestStreamArn, ShardId: sh.ShardId, ShardIteratorType: sttypes.ShardIteratorTypeTrimHorizon})
		if err != nil {
			t.Fatal(err)
		}
		iter := it.ShardIterator
		for i := 0; iter != nil && i < 20; i++ {
			res, err := sc.GetRecords(ctx, &dynamodbstreams.GetRecordsInput{ShardIterator: iter})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range res.Records {
				out = append(out, events.DynamoDBEventRecord{
					EventName: string(r.EventName),
					Change: events.DynamoDBStreamRecord{
						SequenceNumber: aws.ToString(r.Dynamodb.SequenceNumber),
						NewImage:       convert(r.Dynamodb.NewImage), OldImage: convert(r.Dynamodb.OldImage),
					},
				})
			}
			if len(res.Records) == 0 {
				break
			}
			iter = res.NextShardIterator
		}
	}
	return out
}

func convert(img map[string]sttypes.AttributeValue) map[string]events.DynamoDBAttributeValue {
	out := map[string]events.DynamoDBAttributeValue{}
	for k, v := range img {
		switch a := v.(type) {
		case *sttypes.AttributeValueMemberS:
			out[k] = events.NewStringAttribute(a.Value)
		case *sttypes.AttributeValueMemberN:
			out[k] = events.NewNumberAttribute(a.Value)
		case *sttypes.AttributeValueMemberBOOL:
			out[k] = events.NewBooleanAttribute(a.Value)
		}
	}
	return out
}

type mailbox struct{ sent []notify.Email }

func (m *mailbox) Send(_ context.Context, e notify.Email) (string, error) {
	m.sent = append(m.sent, e)
	return "ses-msg-1", nil
}

func TestStreamsDeliverOutboxAndPurgeDeletedProducts(t *testing.T) {
	ctx := context.Background()
	db, table := testutil.NewTable(t)
	st := store.New(db, table)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// A product whose check raised an alert: the commit writes the
	// notification in the same transaction (ADR-004).
	if _, err := st.UpsertProfile(ctx, "alice", "alice@example.com", t0); err != nil {
		t.Fatal(err)
	}
	next := t0.Add(time.Hour)
	p := domain.Product{ID: "P1", UserID: "alice", Name: "Air Fryer", URL: "https://shop.example.com/p/1", URLHash: "h1",
		Target: domain.Money{Minor: 10000, Currency: "USD"}, Frequency: domain.Frequency(6 * time.Hour),
		Status: domain.StatusActive, AlertState: domain.AlertArmed, NextCheckAt: &next, Version: 1, CreatedAt: t0, UpdatedAt: t0}
	if err := st.CreateProduct(ctx, p, domain.Check{ID: "C1", ProductID: "P1", UserID: "alice", Status: domain.CheckQueued, QueuedAt: t0}, 10); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BeginCheck(ctx, domain.CheckMessage{UserID: "alice", ProductID: "P1", CheckID: "C1"}, t0, time.Minute)
	price := domain.Money{Minor: 9400, Currency: "USD"}
	p.Current, p.AlertState = &price, domain.AlertTriggered
	if err := st.CommitCheck(ctx, store.CheckCommit{
		Check:   domain.Check{ID: "C1", ProductID: "P1", UserID: "alice", Status: domain.CheckSucceeded, Outcome: domain.OutcomeOK},
		Product: &p, ExpectedVersion: 1,
		Point:        &domain.PricePoint{ProductID: "P1", ObservedAt: t0, Price: price, Availability: domain.InStock},
		Notification: &domain.Notification{ID: "N1", UserID: "alice", ProductID: "P1", ProductName: p.Name, ProductURL: p.URL, Price: price, Target: p.Target, Status: domain.NotificationPending, CreatedAt: t0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteProduct(ctx, "alice", "P1"); err != nil {
		t.Fatal(err)
	}

	mail := &mailbox{}
	dispatcher := notify.NewDispatcher(st, mail, nil, "https://app.example", log)
	handler := lambdax.StreamHandler(lambdax.StreamHandlers{
		NotificationCreated: dispatcher.Dispatch,
		ProductDeleted:      func(ctx context.Context, id string) error { _, err := st.PurgeProductData(ctx, id); return err },
		Log:                 log,
	})

	records := readStream(t, db, table)
	if len(records) < 5 {
		t.Fatalf("stream has %d records; expected the full write history", len(records))
	}
	resp, err := handler(ctx, events.DynamoDBEvent{Records: records})
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("handler: %+v %v", resp, err)
	}

	if len(mail.sent) != 1 || mail.sent[0].To != "alice@example.com" {
		t.Fatalf("emails = %+v", mail.sent)
	}
	n, _ := st.GetNotification(ctx, "alice", "N1")
	if n.Status != domain.NotificationSent || n.ProviderID != "ses-msg-1" {
		t.Fatalf("notification %+v", n)
	}
	pts, _, _ := st.ListPrices(ctx, store.PriceQuery{ProductID: "P1", From: t0.Add(-time.Hour), To: t0.Add(time.Hour), Limit: 10})
	if chk, err := st.GetCheck(ctx, "P1", "C1"); len(pts) != 0 || err == nil {
		t.Fatalf("history not purged: %d points, check %+v", len(pts), chk)
	}

	// Lambda retries a whole batch after a timeout: replaying it must not
	// send a second email.
	if _, err := handler(ctx, events.DynamoDBEvent{Records: records}); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("replayed stream sent %d emails", len(mail.sent))
	}
}
