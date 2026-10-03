package fetch

import (
	"errors"
	"fmt"
	"time"
)

// Kind classifies fetch failures so callers can decide what to do without
// inspecting status codes or error strings.
type Kind string

const (
	KindTransient   Kind = "TRANSIENT"    // network error, timeout, 5xx: retry later
	KindRateLimited Kind = "RATE_LIMITED" // 429: retry after RetryAfter
	KindBlocked     Kind = "BLOCKED"      // 401/403: likely bot protection
	KindNotFound    Kind = "NOT_FOUND"    // 404/410: product page removed
	KindHTTP        Kind = "HTTP"         // other unexpected status
	KindForbidden   Kind = "FORBIDDEN"    // SSRF guard refused the destination
	KindDisallowed  Kind = "DISALLOWED"   // robots.txt disallows the path
	KindTooLarge    Kind = "TOO_LARGE"    // body exceeded the size cap
	KindUnsupported Kind = "UNSUPPORTED"  // content type is not HTML/JSON
)

// Error is the single error type returned by Client.Get.
type Error struct {
	Kind       Kind
	Status     int           // HTTP status, if a response was received
	RetryAfter time.Duration // from Retry-After, if present
	URL        string
	Err        error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("fetch %s: %s", e.URL, e.Kind)
	if e.Status != 0 {
		msg += fmt.Sprintf(" (status %d)", e.Status)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Retryable reports whether an immediate in-request retry is worthwhile.
func (e *Error) Retryable() bool {
	return e.Kind == KindTransient || e.Kind == KindRateLimited
}

// AsError extracts a *Error from err.
func AsError(err error) (*Error, bool) {
	var fe *Error
	ok := errors.As(err, &fe)
	return fe, ok
}
