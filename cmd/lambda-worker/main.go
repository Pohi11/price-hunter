// Command lambda-worker processes batches of check messages from SQS.
package main

import (
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Pohi11/price-hunter/internal/lambdax"
)

func main() {
	a := lambdax.MustBuild("worker")
	lambda.Start(lambdax.SQSBatchHandler(a.Checker.Run, lambdax.SQSBatchOptions{
		Concurrency: a.Config.WorkerConcurrency,
		// A check needs up to PH_CHECK_TIMEOUT plus commit time. Messages that
		// can't start with that much time left go back to the queue.
		Reserve:    a.Config.CheckTimeout + 5*time.Second,
		Log:        a.Log,
		Visibility: sqs.NewFromConfig(a.AWS),
	}))
}
