package domain

import (
	"fmt"
	"time"
)

// ProductStatus is the lifecycle state of a tracked product.
type ProductStatus string

const (
	StatusActive         ProductStatus = "ACTIVE"
	StatusPaused         ProductStatus = "PAUSED"
	StatusNeedsAttention ProductStatus = "NEEDS_ATTENTION" // repeated extraction failures
	StatusGone           ProductStatus = "GONE"            // retailer says the page no longer exists
)

// Schedulable reports whether the scheduler should keep checking products in this status.
func (s ProductStatus) Schedulable() bool {
	return s == StatusActive || s == StatusNeedsAttention
}

// Availability is the stock state reported by the retailer.
type Availability string

const (
	InStock             Availability = "IN_STOCK"
	OutOfStock          Availability = "OUT_OF_STOCK"
	AvailabilityUnknown Availability = "UNKNOWN"
)

// Frequency is how often a product is checked. Only a fixed set is allowed
// so retailer load and cost stay predictable.
type Frequency time.Duration

var allowedFrequencies = map[string]Frequency{
	"1h":  Frequency(time.Hour),
	"6h":  Frequency(6 * time.Hour),
	"12h": Frequency(12 * time.Hour),
	"24h": Frequency(24 * time.Hour),
}

// ParseFrequency accepts "1h", "6h", "12h" or "24h".
func ParseFrequency(s string) (Frequency, error) {
	f, ok := allowedFrequencies[s]
	if !ok {
		return 0, fmt.Errorf("frequency must be one of 1h, 6h, 12h, 24h")
	}
	return f, nil
}

// Duration returns the frequency as a time.Duration.
func (f Frequency) Duration() time.Duration { return time.Duration(f) }

// String renders the frequency in its API form ("6h").
func (f Frequency) String() string {
	return fmt.Sprintf("%dh", int(time.Duration(f).Hours()))
}

// Product is a user's tracked product plus the tracking state the worker maintains.
type Product struct {
	ID           string
	UserID       string
	Name         string
	URL          string // URL as the user entered it (tracking params stripped)
	CanonicalURL string // normalized form used for duplicate detection
	URLHash      string
	Retailer     string // retailer profile ID, e.g. "books-toscrape" or "generic"
	Host         string // hostname used for rate limiting and circuit breaking

	Target    Money
	Frequency Frequency
	Status    ProductStatus

	// Tracking state, written by the checker.
	Current             *Money
	Lowest              *Money
	Availability        Availability
	AlertState          AlertState
	LastAlertAt         *time.Time
	LastCheckedAt       *time.Time
	LastSuccessAt       *time.Time
	LastErrorCode       OutcomeCode
	ConsecutiveFailures int
	PendingSuspect      *Money // anomalous price awaiting confirmation
	NextCheckAt         *time.Time
	LastManualCheckAt   *time.Time

	Version   int // incremented on user edits only; used for If-Match
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsStale reports whether the last successful price is older than three check intervals.
func (p Product) IsStale(now time.Time) bool {
	if p.LastSuccessAt == nil {
		return p.LastCheckedAt != nil
	}
	return now.Sub(*p.LastSuccessAt) > 3*p.Frequency.Duration()
}

// PricePoint is one successful price observation.
type PricePoint struct {
	ProductID    string
	ObservedAt   time.Time
	Price        Money
	Availability Availability
	Strategy     string // extractor that produced the price: jsonld, meta, selectors
	CheckID      string
	Suspect      bool // anomalous value not yet confirmed; excluded from alerts
}
