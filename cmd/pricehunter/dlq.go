package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Pohi11/price-hunter/internal/config"
	"github.com/Pohi11/price-hunter/internal/queue"
)

// runDLQ implements "pricehunter dlq peek|redrive". Messages land in the
// DLQ only after repeated infrastructure failures (ADR-006): inspect them,
// fix the cause, then redrive.
func runDLQ(args []string) error {
	if len(args) == 0 || (args[0] != "peek" && args[0] != "redrive") {
		return errors.New("usage: pricehunter dlq peek|redrive [-max N]")
	}
	fs := flag.NewFlagSet("dlq", flag.ExitOnError)
	max := fs.Int("max", 100, "maximum messages")
	_ = fs.Parse(args[1:])

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.CheckDLQURL == "" || cfg.CheckQueueURL == "" {
		return errors.New("set PH_CHECK_QUEUE_URL and PH_CHECK_DLQ_URL (see terraform outputs)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := sqsClient(ctx, cfg)
	if err != nil {
		return err
	}

	if args[0] == "peek" {
		msgs, err := queue.Peek(ctx, client, cfg.CheckDLQURL, *max)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			fmt.Printf("%s receives=%d %s\n", m.MessageID, m.Receives, m.Body)
		}
		fmt.Fprintf(os.Stderr, "%d message(s) in DLQ\n", len(msgs))
		return nil
	}
	n, err := queue.Redrive(ctx, client, cfg.CheckDLQURL, cfg.CheckQueueURL, *max)
	fmt.Fprintf(os.Stderr, "redrove %d message(s)\n", n)
	return err
}

func sqsClient(ctx context.Context, cfg config.Config) (*sqs.Client, error) {
	if cfg.SQSEndpoint != "" {
		return sqs.NewFromConfig(aws.Config{Region: cfg.Region, Credentials: credentials.NewStaticCredentialsProvider("local", "local", "")},
			func(o *sqs.Options) { o.BaseEndpoint = aws.String(cfg.SQSEndpoint) }), nil
	}
	ac, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(ac), nil
}
