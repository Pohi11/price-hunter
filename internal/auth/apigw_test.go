package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
)

func loadEvent(t *testing.T) events.APIGatewayV2HTTPRequest {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/events/apigw-v2-list-products.json")
	if err != nil {
		t.Fatal(err)
	}
	var ev events.APIGatewayV2HTTPRequest
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// proxy runs ev through the real Lambda adapter into a handler that echoes
// the resolved identity.
func proxy(t *testing.T, ev events.APIGatewayV2HTTPRequest) (int, Identity) {
	t.Helper()
	var got Identity
	unauthorized := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	h := Require(APIGateway(), unauthorized, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = FromContext(r.Context())
		if r.URL.Path != "/v1/products" || r.URL.Query().Get("limit") != "10" {
			t.Errorf("request not translated: %s", r.URL)
		}
		w.WriteHeader(200)
	}))
	resp, err := httpadapter.NewV2(h).ProxyWithContext(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, got
}

func TestAPIGatewayClaims(t *testing.T) {
	status, id := proxy(t, loadEvent(t))
	if status != 200 || id.UserID != "8f2e3a10-1234-4bcd-9abc-0123456789ab" || id.Email != "alice@example.com" {
		t.Fatalf("status %d identity %+v", status, id)
	}
}

func TestAPIGatewayUnverifiedEmailIgnored(t *testing.T) {
	ev := loadEvent(t)
	ev.RequestContext.Authorizer.JWT.Claims["email_verified"] = "false"
	_, id := proxy(t, ev)
	if id.UserID == "" || id.Email != "" {
		t.Fatalf("unverified email trusted: %+v", id)
	}
}

func TestAPIGatewayMissingAuthorizerIsUnauthorized(t *testing.T) {
	ev := loadEvent(t)
	ev.RequestContext.Authorizer = nil // e.g. a route accidentally deployed without the JWT authorizer
	if status, _ := proxy(t, ev); status != 401 {
		t.Fatalf("status %d, want 401", status)
	}
	ev = loadEvent(t)
	ev.RequestContext.Authorizer.JWT.Claims["sub"] = "../../evil"
	if status, _ := proxy(t, ev); status != 401 {
		t.Fatalf("malformed sub accepted: %d", status)
	}
}
