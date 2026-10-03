package retailer

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/extract"
	"github.com/Pohi11/price-hunter/internal/fetch"
)

// Quote is the result of looking up a product's current price.
type Quote struct {
	Offer         extract.Offer
	Retailer      string // profile ID
	FinalURL      string
	Disagreement  bool          // extraction strategies disagreed on the price
	FetchDuration time.Duration // time spent fetching (incl. retries)
	Body          []byte        // raw page; kept so failures can be snapshotted
}

// Fetcher is the subset of *fetch.Client a Source needs.
type Fetcher interface {
	Get(ctx context.Context, rawURL string) (*fetch.Response, error)
}

// Source fetches a product page and extracts its price.
type Source struct {
	fetcher  Fetcher
	registry *Registry
}

// NewSource builds a Source.
func NewSource(f Fetcher, r *Registry) *Source {
	return &Source{fetcher: f, registry: r}
}

// Quote fetches rawURL and extracts an offer. On failure the returned Quote
// still carries whatever is known (retailer, body) for diagnostics; use
// Classify to turn the error into an outcome code.
func (s *Source) Quote(ctx context.Context, rawURL string) (Quote, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Quote{}, &fetch.Error{Kind: fetch.KindForbidden, URL: rawURL, Err: err}
	}
	profile, err := s.registry.Lookup(u.Host)
	if err != nil {
		return Quote{}, err
	}
	q := Quote{Retailer: profile.ID}

	resp, err := s.fetcher.Get(ctx, rawURL)
	if err != nil {
		return q, err
	}
	q.FinalURL, q.FetchDuration, q.Body = resp.URL, resp.Duration, resp.Body

	doc, err := extract.Parse(resp.Body, resp.URL)
	if err != nil {
		return q, extract.ErrNoPrice
	}
	res, err := profile.Pipeline().Run(doc, profile.Currency)
	if err != nil {
		return q, err
	}
	q.Offer, q.Disagreement = res.Offer, res.Disagreement
	return q, nil
}

// Classify maps a Quote error to a check outcome and an optional minimum
// delay before the next attempt.
func Classify(err error) (domain.OutcomeCode, time.Duration) {
	if err == nil {
		return domain.OutcomeOK, 0
	}
	if fe, ok := fetch.AsError(err); ok {
		switch fe.Kind {
		case fetch.KindTransient:
			return domain.OutcomeFetchTransient, fe.RetryAfter
		case fetch.KindRateLimited:
			return domain.OutcomeRateLimited, fe.RetryAfter
		case fetch.KindBlocked:
			return domain.OutcomeBlocked, 0
		case fetch.KindNotFound:
			return domain.OutcomeNotFound, 0
		case fetch.KindForbidden:
			return domain.OutcomeForbiddenTarget, 0
		case fetch.KindDisallowed:
			return domain.OutcomeRobotsDisallowed, 0
		case fetch.KindTooLarge:
			return domain.OutcomeTooLarge, 0
		case fetch.KindUnsupported:
			return domain.OutcomeUnsupportedContent, 0
		default:
			return domain.OutcomeHTTPError, 0
		}
	}
	switch {
	case errors.Is(err, extract.ErrAmbiguous):
		return domain.OutcomeAmbiguousPrice, 0
	case errors.Is(err, extract.ErrImplausible):
		return domain.OutcomeImplausiblePrice, 0
	case errors.Is(err, extract.ErrNoPrice):
		return domain.OutcomePriceNotFound, 0
	case errors.Is(err, domain.ErrUnsupportedRetailer):
		return domain.OutcomeForbiddenTarget, 0
	}
	// Context cancellation and anything unexpected: try again later.
	return domain.OutcomeFetchTransient, 0
}
