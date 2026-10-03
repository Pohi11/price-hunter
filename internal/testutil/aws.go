// Package testutil provides helpers for integration tests that run against
// DynamoDB Local and ElasticMQ (docker compose up -d).
package testutil

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Pohi11/price-hunter/internal/store"
)

// Endpoints, overridable for CI service containers.
func DynamoEndpoint() string { return envOr("PH_TEST_DYNAMODB_ENDPOINT", "http://localhost:8000") }
func SQSEndpoint() string    { return envOr("PH_TEST_SQS_ENDPOINT", "http://localhost:9324") }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// LocalAWSConfig is an aws.Config with static dummy credentials.
func LocalAWSConfig() aws.Config {
	return aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("local", "local", ""),
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// requireUp skips the test when the endpoint is unreachable, so `go test
// -tags integration` gives a clear message instead of timeouts.
func requireUp(t *testing.T, endpoint string) {
	t.Helper()
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(endpoint) //nolint:noctx // test probe
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s unreachable in CI: %v", endpoint, err)
		}
		t.Skipf("%s unreachable (run: docker compose up -d): %v", endpoint, err)
	}
	_ = resp.Body.Close()
}

// DynamoClient returns a client for DynamoDB Local.
func DynamoClient(t *testing.T) *dynamodb.Client {
	t.Helper()
	requireUp(t, DynamoEndpoint())
	return dynamodb.NewFromConfig(LocalAWSConfig(), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(DynamoEndpoint())
	})
}

var unsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// NewTable creates a uniquely named table for one test and deletes it afterwards.
func NewTable(t *testing.T) (*dynamodb.Client, string) {
	t.Helper()
	db := DynamoClient(t)
	name := fmt.Sprintf("t-%s-%d", unsafe.ReplaceAllString(t.Name(), "_"), rand.IntN(1_000_000))
	if len(name) > 200 {
		name = name[:200]
	}
	ctx := context.Background()
	if err := store.EnsureTable(ctx, db, name); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
	})
	return db, name
}

// SQSClient returns a client for ElasticMQ.
func SQSClient(t *testing.T) *sqs.Client {
	t.Helper()
	requireUp(t, SQSEndpoint())
	return sqs.NewFromConfig(LocalAWSConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(SQSEndpoint())
	})
}
