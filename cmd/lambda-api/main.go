// Command lambda-api serves the REST API behind API Gateway HTTP API.
// The same net/http handler as server mode, wrapped by an adapter.
package main

import (
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/Pohi11/price-hunter/internal/auth"
	"github.com/Pohi11/price-hunter/internal/lambdax"
)

func main() {
	a := lambdax.MustBuild("api")
	adapter := httpadapter.NewV2(a.APIHandler(auth.APIGateway()))
	lambda.Start(adapter.ProxyWithContext)
}
