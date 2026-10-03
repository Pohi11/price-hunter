package lambdax

import (
	"context"
	"os"
	"time"

	"github.com/Pohi11/price-hunter/internal/app"
	"github.com/Pohi11/price-hunter/internal/config"
	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// MustBuild loads config and wires the app during the Lambda init phase.
// Work done here is reused across warm invocations. A configuration error
// fails the init (visible in CloudWatch) rather than every request.
func MustBuild(component string) *app.App {
	cfg, err := config.Load()
	log := telemetry.NewLogger(os.Stdout, cfg.LogLevel, "service", "pricehunter", "component", component,
		"env", string(cfg.Env), "version", cfg.Version)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	a, err := app.Build(ctx, cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	return a
}
