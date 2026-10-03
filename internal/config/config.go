// Package config loads typed configuration from environment variables and
// validates it at startup, so a misconfigured deployment fails immediately.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env is the deployment environment.
type Env string

const (
	EnvLocal Env = "local"
	EnvDev   Env = "dev"
	EnvProd  Env = "prod"
)

// Config is every setting, grouped by concern. Field comments name the variable.
type Config struct {
	Env      Env    // PH_ENV: local | dev | prod
	LogLevel string // PH_LOG_LEVEL
	Version  string // PH_VERSION (git SHA, set by the deploy)
	Metrics  bool   // PH_METRICS: emit CloudWatch EMF metric lines to stdout

	// HTTP (server mode)
	HTTPAddr    string   // PH_HTTP_ADDR
	CORSOrigins []string // PH_CORS_ORIGINS (comma-separated)
	WebDir      string   // PH_WEB_DIR: serve the built SPA from here (local)

	// AWS
	Region         string // AWS_REGION
	Table          string // PH_TABLE
	DynamoEndpoint string // PH_DYNAMODB_ENDPOINT (DynamoDB Local)
	CheckQueueURL  string // PH_CHECK_QUEUE_URL; empty = in-memory queue (local)
	CheckDLQURL    string // PH_CHECK_DLQ_URL (DLQ tooling)
	SQSEndpoint    string // PH_SQS_ENDPOINT (ElasticMQ)
	SnapshotBucket string // PH_SNAPSHOT_BUCKET; empty = PH_SNAPSHOT_DIR on disk
	SnapshotDir    string // PH_SNAPSHOT_DIR

	// Email
	EmailMode           string // PH_EMAIL_MODE: log | ses
	EmailFrom           string // PH_EMAIL_FROM
	SESConfigurationSet string // PH_SES_CONFIGURATION_SET
	AppBaseURL          string // PH_APP_BASE_URL: links in emails

	// Retailers / fetching
	AllowGeneric   bool   // PH_ALLOW_GENERIC: accept hosts without a profile
	AllowLocalhost bool   // PH_ALLOW_LOCALHOST: demo store on localhost (local only)
	DemostoreHost  string // PH_DEMOSTORE_HOST: extra host for the demostore profile

	// Limits
	MaxProducts    int           // PH_MAX_PRODUCTS per user
	ManualCooldown time.Duration // PH_MANUAL_COOLDOWN

	// Worker
	WorkerConcurrency int           // PH_WORKER_CONCURRENCY
	CheckTimeout      time.Duration // PH_CHECK_TIMEOUT per check

	// Scheduler
	SchedulerInterval  time.Duration // PH_SCHEDULER_INTERVAL (server mode ticker)
	SchedulerMaxPerRun int           // PH_SCHEDULER_MAX_PER_RUN
	MaxQueueDepth      int           // PH_SCHEDULER_MAX_QUEUE_DEPTH: backpressure threshold

	// Local auth
	DevUser  string // PH_DEV_USER
	DevEmail string // PH_DEV_EMAIL
}

// Load reads the environment.
func Load() (Config, error) {
	var errs []error
	c := Config{
		Env:      Env(get("PH_ENV", "local")),
		LogLevel: get("PH_LOG_LEVEL", "info"),
		Version:  get("PH_VERSION", "dev"),
		Metrics:  boolean("PH_METRICS", false, &errs),

		HTTPAddr:    get("PH_HTTP_ADDR", ":8088"),
		CORSOrigins: list(get("PH_CORS_ORIGINS", "")),
		WebDir:      get("PH_WEB_DIR", ""),

		Region:         get("AWS_REGION", "us-east-1"),
		Table:          get("PH_TABLE", "pricehunter-local"),
		DynamoEndpoint: get("PH_DYNAMODB_ENDPOINT", ""),
		CheckQueueURL:  get("PH_CHECK_QUEUE_URL", ""),
		CheckDLQURL:    get("PH_CHECK_DLQ_URL", ""),
		SQSEndpoint:    get("PH_SQS_ENDPOINT", ""),
		SnapshotBucket: get("PH_SNAPSHOT_BUCKET", ""),
		SnapshotDir:    get("PH_SNAPSHOT_DIR", "data/snapshots"),

		EmailMode:           get("PH_EMAIL_MODE", "log"),
		EmailFrom:           get("PH_EMAIL_FROM", ""),
		SESConfigurationSet: get("PH_SES_CONFIGURATION_SET", ""),
		AppBaseURL:          get("PH_APP_BASE_URL", "http://localhost:5173"),

		AllowGeneric:   boolean("PH_ALLOW_GENERIC", true, &errs),
		AllowLocalhost: boolean("PH_ALLOW_LOCALHOST", false, &errs),
		DemostoreHost:  get("PH_DEMOSTORE_HOST", ""),

		MaxProducts:    integer("PH_MAX_PRODUCTS", 50, &errs),
		ManualCooldown: duration("PH_MANUAL_COOLDOWN", 5*time.Minute, &errs),

		WorkerConcurrency: integer("PH_WORKER_CONCURRENCY", 5, &errs),
		CheckTimeout:      duration("PH_CHECK_TIMEOUT", 25*time.Second, &errs),

		SchedulerInterval:  duration("PH_SCHEDULER_INTERVAL", time.Minute, &errs),
		SchedulerMaxPerRun: integer("PH_SCHEDULER_MAX_PER_RUN", 500, &errs),
		MaxQueueDepth:      integer("PH_SCHEDULER_MAX_QUEUE_DEPTH", 2000, &errs),

		DevUser:  get("PH_DEV_USER", "local-user"),
		DevEmail: get("PH_DEV_EMAIL", "you@example.com"),
	}
	errs = append(errs, c.Validate())
	return c, errors.Join(errs...)
}

// Validate enforces cross-field rules.
func (c Config) Validate() error {
	var errs []error
	switch c.Env {
	case EnvLocal, EnvDev, EnvProd:
	default:
		errs = append(errs, fmt.Errorf("PH_ENV must be local, dev or prod (got %q)", c.Env))
	}
	if c.AllowLocalhost && c.Env != EnvLocal {
		// Loopback hosts the Lambda runtime API; never reachable from the fetcher in AWS.
		errs = append(errs, errors.New("PH_ALLOW_LOCALHOST is only permitted when PH_ENV=local"))
	}
	if c.Env != EnvLocal {
		if c.DynamoEndpoint != "" || c.SQSEndpoint != "" {
			errs = append(errs, errors.New("endpoint overrides are only permitted when PH_ENV=local"))
		}
		if c.EmailMode == "ses" && c.EmailFrom == "" {
			errs = append(errs, errors.New("PH_EMAIL_FROM is required when PH_EMAIL_MODE=ses"))
		}
	}
	if c.EmailMode != "log" && c.EmailMode != "ses" {
		errs = append(errs, fmt.Errorf("PH_EMAIL_MODE must be log or ses (got %q)", c.EmailMode))
	}
	if c.Table == "" {
		errs = append(errs, errors.New("PH_TABLE is required"))
	}
	if c.WorkerConcurrency < 1 || c.WorkerConcurrency > 64 {
		errs = append(errs, errors.New("PH_WORKER_CONCURRENCY must be 1-64"))
	}
	if c.MaxProducts < 1 {
		errs = append(errs, errors.New("PH_MAX_PRODUCTS must be positive"))
	}
	return errors.Join(errs...)
}

// IsLocal reports whether this is a developer machine.
func (c Config) IsLocal() bool { return c.Env == EnvLocal }

func get(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func boolean(k string, def bool, errs *[]error) bool {
	v := get(k, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", k, err))
	}
	return b
}

func integer(k string, def int, errs *[]error) int {
	v := get(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", k, err))
	}
	return n
}

func duration(k string, def time.Duration, errs *[]error) time.Duration {
	v := get(k, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", k, err))
	}
	return d
}
