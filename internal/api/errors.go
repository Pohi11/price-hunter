package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/aws/smithy-go"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/store"
)

// problem is an RFC 9457 problem details body.
type problem struct {
	Type     string              `json:"type"`
	Title    string              `json:"title"`
	Status   int                 `json:"status"`
	Detail   string              `json:"detail,omitempty"`
	Instance string              `json:"instance,omitempty"`
	Errors   []domain.FieldError `json:"errors,omitempty"`
	// Extension members
	ExistingProductID string `json:"existing_product_id,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
}

func problemType(slug string) string { return "urn:pricehunter:problem:" + slug }

func writeProblem(w http.ResponseWriter, r *http.Request, p problem) {
	p.Instance = r.URL.Path
	p.RequestID = requestIDFrom(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// writeError is the single place errors become HTTP responses. Internal
// details are logged, never returned.
func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *domain.ValidationError
	var dup *store.DuplicateError
	var cd *domain.CooldownError
	switch {
	case errors.As(err, &verr):
		writeProblem(w, r, problem{Type: problemType("validation"), Title: "Invalid request", Status: http.StatusUnprocessableEntity, Errors: verr.Fields})
	case errors.As(err, &dup):
		writeProblem(w, r, problem{Type: problemType("duplicate"), Title: "Product already tracked", Status: http.StatusConflict,
			Detail: "You are already tracking this URL.", ExistingProductID: dup.ExistingProductID})
	case errors.As(err, &cd):
		w.Header().Set("Retry-After", strconv.Itoa(cd.RetryAfterSeconds))
		writeProblem(w, r, problem{Type: problemType("cooldown"), Title: "Too many checks", Status: http.StatusTooManyRequests,
			Detail: "A manual check ran recently. Try again later."})
	case errors.Is(err, domain.ErrNotFound):
		// Also returned for other users' products: never reveal existence.
		writeProblem(w, r, problem{Type: problemType("not-found"), Title: "Not found", Status: http.StatusNotFound})
	case errors.Is(err, domain.ErrQuotaExceeded):
		writeProblem(w, r, problem{Type: problemType("quota"), Title: "Product limit reached", Status: http.StatusForbidden,
			Detail: "Delete a product before adding another."})
	case errors.Is(err, domain.ErrVersionConflict):
		writeProblem(w, r, problem{Type: problemType("version-conflict"), Title: "Product was modified", Status: http.StatusPreconditionFailed,
			Detail: "Reload the product and try again."})
	case errors.Is(err, store.ErrInvalidCursor):
		writeProblem(w, r, problem{Type: problemType("bad-cursor"), Title: "Invalid cursor", Status: http.StatusBadRequest})
	case isTransient(err):
		a.log.WarnContext(r.Context(), "transient backend failure", "err", err)
		w.Header().Set("Retry-After", "5")
		writeProblem(w, r, problem{Type: problemType("unavailable"), Title: "Temporarily unavailable", Status: http.StatusServiceUnavailable})
	default:
		a.log.ErrorContext(r.Context(), "unhandled error", "err", err, "path", r.URL.Path)
		writeProblem(w, r, problem{Type: problemType("internal"), Title: "Internal error", Status: http.StatusInternalServerError})
	}
}

// isTransient recognizes throttling and AWS-side failures worth retrying.
func isTransient(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "ProvisionedThroughputExceededException", "ThrottlingException", "RequestLimitExceeded",
			"InternalServerError", "ServiceUnavailable", "TransactionConflictException":
			return true
		}
	}
	return false
}

func badRequest(w http.ResponseWriter, r *http.Request, detail string) {
	writeProblem(w, r, problem{Type: problemType("bad-request"), Title: "Bad request", Status: http.StatusBadRequest, Detail: detail})
}
