// Package auth puts the caller's identity on the request context. The user
// ID always comes from a verified source (a Cognito JWT validated by API
// Gateway, or the dev header locally) and never from request bodies or paths.
package auth

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// Identity is the authenticated caller.
type Identity struct {
	UserID string // Cognito "sub"
	Email  string // verified email claim, may be empty
}

type ctxKey struct{}

// WithIdentity returns ctx carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the identity, if any.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok && id.UserID != ""
}

// Resolver extracts the identity from a request; it returns false when the
// request is unauthenticated.
type Resolver func(r *http.Request) (Identity, bool)

var validUserID = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,128}$`)

// Dev returns a resolver for local development: the X-Dev-User header, or a
// default user. Never wired up outside PH_ENV=local.
func Dev(defaultUser, defaultEmail string) Resolver {
	return func(r *http.Request) (Identity, bool) {
		user := strings.TrimSpace(r.Header.Get("X-Dev-User"))
		email := strings.TrimSpace(r.Header.Get("X-Dev-Email"))
		if user == "" {
			user, email = defaultUser, defaultEmail
		}
		if !validUserID.MatchString(user) {
			return Identity{}, false
		}
		return Identity{UserID: user, Email: email}, true
	}
}

// Require wraps next so that unauthenticated requests get a 401 and
// authenticated ones carry their Identity in the context.
func Require(resolve Resolver, unauthorized http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := resolve(r)
		if !ok {
			unauthorized.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}
