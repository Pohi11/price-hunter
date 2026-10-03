// Command lambda-streams reacts to DynamoDB Streams: it delivers outbox
// notifications (price alerts) and purges history of deleted products.
package main

import (
	"context"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/Pohi11/price-hunter/internal/lambdax"
)

func main() {
	a := lambdax.MustBuild("streams")
	lambda.Start(lambdax.StreamHandler(lambdax.StreamHandlers{
		NotificationCreated: a.Dispatcher.Dispatch,
		ProductDeleted: func(ctx context.Context, productID string) error {
			n, err := a.Store.PurgeProductData(ctx, productID)
			if err == nil {
				a.Log.InfoContext(ctx, "purged deleted product's data", "product_id", productID, "items", n)
			}
			return err
		},
		Log: a.Log,
	}))
}
