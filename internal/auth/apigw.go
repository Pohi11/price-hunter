package auth

import (
	"net/http"

	"github.com/awslabs/aws-lambda-go-api-proxy/core"
)

// APIGateway resolves identity from claims that API Gateway's JWT
// authorizer has already validated (signature, issuer, audience, expiry).
// Requests only reach the Lambda if validation passed; this reads the
// verified claims the adapter placed on the request context.
func APIGateway() Resolver {
	return func(r *http.Request) (Identity, bool) {
		rc, ok := core.GetAPIGatewayV2ContextFromContext(r.Context())
		if !ok || rc.Authorizer == nil || rc.Authorizer.JWT == nil {
			return Identity{}, false
		}
		claims := rc.Authorizer.JWT.Claims
		sub := claims["sub"]
		if !validUserID.MatchString(sub) {
			return Identity{}, false
		}
		id := Identity{UserID: sub}
		// Only trust the email if Cognito says it is verified. Access tokens
		// don't carry email, so the SPA sends the ID token.
		if claims["email_verified"] == "true" {
			id.Email = claims["email"]
		}
		return id, true
	}
}
