// Command lambda-demostore serves the fictional demo retailer from a
// Lambda Function URL (dev only), giving cloud workers a reliable,
// ToS-friendly target for demos, smoke tests and load tests.
//
// Admin overrides are in-memory per Lambda instance, so they are best-effort
// in the cloud; the deterministic fault products (/p/flaky-blender, ...)
// behave identically on every instance.
package main

import (
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/Pohi11/price-hunter/internal/demostore"
)

func main() {
	h := demostore.New(demostore.Options{AdminToken: os.Getenv("DEMOSTORE_ADMIN_TOKEN")})
	// Function URL events use the API Gateway v2 payload format.
	lambda.Start(httpadapter.NewV2(h).ProxyWithContext)
}
