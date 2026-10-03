// Package app is the composition root: it turns a Config into wired-up
// services. Every entrypoint (server mode, each Lambda) calls Build, so all
// of them run the same code with the same configuration rules.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Pohi11/price-hunter/internal/api"
	"github.com/Pohi11/price-hunter/internal/auth"
	"github.com/Pohi11/price-hunter/internal/checker"
	"github.com/Pohi11/price-hunter/internal/config"
	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/fetch"
	"github.com/Pohi11/price-hunter/internal/metrics"
	"github.com/Pohi11/price-hunter/internal/notify"
	"github.com/Pohi11/price-hunter/internal/products"
	"github.com/Pohi11/price-hunter/internal/queue"
	"github.com/Pohi11/price-hunter/internal/retailer"
	"github.com/Pohi11/price-hunter/internal/scheduler"
	"github.com/Pohi11/price-hunter/internal/snapshot"
	"github.com/Pohi11/price-hunter/internal/store"
	"github.com/Pohi11/price-hunter/internal/telemetry"
	"github.com/Pohi11/price-hunter/internal/urlx"
)

// Enqueuer sends check requests to workers.
type Enqueuer interface {
	Enqueue(ctx context.Context, msgs ...domain.CheckMessage) error
}

// App holds the wired dependencies.
type App struct {
	Config   config.Config
	Log      *slog.Logger
	AWS      aws.Config
	Store    *store.Store
	Registry *retailer.Registry
	Queue    Enqueuer
	MemQueue *queue.Mem // non-nil when using the in-process queue
	SQSQueue *queue.SQS // non-nil when using SQS
	Products *products.Service

	Fetcher    *fetch.Client
	Source     *retailer.Source
	Checker    *checker.Checker
	Scheduler  *scheduler.Scheduler
	Dispatcher *notify.Dispatcher
}

// Build wires everything from cfg.
func Build(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	a := &App{Config: cfg, Log: log}

	awsCfg, err := loadAWS(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a.AWS = awsCfg

	db := dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		if cfg.DynamoEndpoint != "" {
			o.BaseEndpoint = aws.String(cfg.DynamoEndpoint)
		}
	})
	if cfg.IsLocal() {
		// In AWS, Terraform owns the table. Locally we create it on startup.
		if err := store.EnsureTable(ctx, db, cfg.Table); err != nil {
			return nil, fmt.Errorf("ensure local table: %w", err)
		}
	}
	a.Store = store.New(db, cfg.Table)

	extra := map[string][]string{}
	if cfg.DemostoreHost != "" {
		extra["demostore"] = []string{cfg.DemostoreHost}
	}
	a.Registry, err = retailer.Default(retailer.Options{AllowGeneric: cfg.AllowGeneric, ExtraDomains: extra})
	if err != nil {
		return nil, err
	}

	if cfg.CheckQueueURL == "" {
		a.MemQueue = queue.NewMem(10_000, 5)
		a.Queue = a.MemQueue
	} else {
		client := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
			if cfg.SQSEndpoint != "" {
				o.BaseEndpoint = aws.String(cfg.SQSEndpoint)
			}
		})
		a.SQSQueue = &queue.SQS{Client: client, QueueURL: cfg.CheckQueueURL}
		a.Queue = a.SQSQueue
	}

	a.Products = products.New(a.Store, a.Queue, a.Registry, products.Config{
		MaxProducts: cfg.MaxProducts, ManualCooldown: cfg.ManualCooldown,
		URLOptions: urlx.Options{AllowLocalhost: cfg.AllowLocalhost},
	}, log)

	// Price checks.
	fopts := fetch.DefaultOptions()
	fopts.Guard = fetch.Guard{AllowLoopback: cfg.AllowLocalhost}
	fopts.HostRate = a.Registry.RateFor
	a.Fetcher = fetch.New(fopts)
	a.Source = retailer.NewSource(a.Fetcher, a.Registry)

	// Metrics (EMF lines on stdout; CloudWatch extracts them in Lambda).
	var rec *metrics.Recorder
	if cfg.Metrics {
		ids := make([]string, 0, len(a.Registry.Profiles()))
		for _, p := range a.Registry.Profiles() {
			ids = append(ids, p.ID)
		}
		rec = metrics.New(telemetry.NewEMF(os.Stdout, metrics.Namespace, string(cfg.Env)), ids)
	}

	// Notifications.
	var sender notify.Sender = notify.LogSender{Log: log, Dir: "data/outbox"}
	if cfg.EmailMode == "ses" {
		sender = notify.SESSender{Client: sesv2.NewFromConfig(awsCfg), From: cfg.EmailFrom, ConfigurationSet: cfg.SESConfigurationSet}
	} else if !cfg.IsLocal() {
		sender = notify.LogSender{Log: log} // no local files in Lambda
	}
	var notifyObs notify.Observer
	if rec != nil {
		notifyObs = rec
	}
	a.Dispatcher = notify.NewDispatcher(a.Store, sender, notifyObs, cfg.AppBaseURL, log)

	ccfg := checker.DefaultConfig()
	ccfg.CheckTimeout = cfg.CheckTimeout
	var snaps checker.Snapshotter = snapshot.Dir{Root: cfg.SnapshotDir}
	if cfg.SnapshotBucket != "" {
		snaps = snapshot.S3{Client: s3.NewFromConfig(awsCfg), Bucket: cfg.SnapshotBucket}
	}
	copts := []checker.Option{checker.WithSnapshots(snaps)}
	if rec != nil {
		copts = append(copts, checker.WithObserver(rec))
	}
	if cfg.IsLocal() {
		// No DynamoDB Streams locally: deliver alerts and purge history inline.
		copts = append(copts, checker.WithDispatcher(a.Dispatcher))
		a.Products.OnDeleted = func(ctx context.Context, productID string) {
			if n, err := a.Store.PurgeProductData(ctx, productID); err != nil {
				log.WarnContext(ctx, "purge product data", "product_id", productID, "err", err)
			} else {
				log.InfoContext(ctx, "purged product data", "product_id", productID, "items", n)
			}
		}
	}
	a.Checker = checker.New(a.Store, a.Source, ccfg, log, copts...)

	var depth scheduler.DepthReader
	switch {
	case a.MemQueue != nil:
		depth = a.MemQueue
	case a.SQSQueue != nil:
		depth = a.SQSQueue
	}
	var schedObs scheduler.Observer
	if rec != nil {
		schedObs = rec
	}
	a.Scheduler = scheduler.New(a.Store, a.Queue, depth, schedObs, scheduler.Config{
		MaxPerRun: cfg.SchedulerMaxPerRun, MaxQueueDepth: cfg.MaxQueueDepth,
	}, log)
	return a, nil
}

func loadAWS(ctx context.Context, cfg config.Config) (aws.Config, error) {
	if cfg.IsLocal() && (cfg.DynamoEndpoint != "" || cfg.SQSEndpoint != "") {
		// Emulators accept any credentials; don't require a real AWS profile.
		return aws.Config{
			Region:      cfg.Region,
			Credentials: credentials.NewStaticCredentialsProvider("local", "local", ""),
		}, nil
	}
	c, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config: %w", err)
	}
	return c, nil
}

// APIHandler builds the HTTP API with the given identity resolver.
func (a *App) APIHandler(resolver auth.Resolver) http.Handler {
	return api.New(a.Products, a.Registry, a.Log, api.Options{
		Auth: resolver, CORSOrigins: a.Config.CORSOrigins, MaxProducts: a.Config.MaxProducts, Version: a.Config.Version,
	})
}
