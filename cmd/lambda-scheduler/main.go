// Command lambda-scheduler runs one scheduling pass per EventBridge
// Scheduler invocation (every 5 minutes).
package main

import (
	"context"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/Pohi11/price-hunter/internal/lambdax"
)

func main() {
	a := lambdax.MustBuild("scheduler")
	lambda.Start(func(ctx context.Context) error {
		// An error makes EventBridge Scheduler retry; that's safe because
		// leases are conditional and the next run would pick up the rest anyway.
		_, err := a.Scheduler.Run(ctx)
		return err
	})
}
