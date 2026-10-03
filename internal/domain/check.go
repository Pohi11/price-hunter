package domain

import "time"

// CheckStatus is the lifecycle of a single price check.
type CheckStatus string

const (
	CheckQueued    CheckStatus = "QUEUED"
	CheckRunning   CheckStatus = "RUNNING"
	CheckSucceeded CheckStatus = "SUCCEEDED"
	CheckFailed    CheckStatus = "FAILED"
	CheckSkipped   CheckStatus = "SKIPPED"
)

// Terminal reports whether no further work will happen for this check.
func (s CheckStatus) Terminal() bool {
	return s == CheckSucceeded || s == CheckFailed || s == CheckSkipped
}

// Trigger records why a check ran.
type Trigger string

const (
	TriggerInitial   Trigger = "INITIAL" // first check, queued when the product is created
	TriggerScheduled Trigger = "SCHEDULED"
	TriggerManual    Trigger = "MANUAL"
	TriggerRecheck   Trigger = "RECHECK" // confirmation of a suspicious price
)

// OutcomeCode classifies the result of a check. Expected failures are
// outcomes (data), not Go errors.
type OutcomeCode string

const (
	OutcomeOK                 OutcomeCode = "OK"
	OutcomePriceNotFound      OutcomeCode = "PRICE_NOT_FOUND"
	OutcomeAmbiguousPrice     OutcomeCode = "AMBIGUOUS_PRICE"
	OutcomeImplausiblePrice   OutcomeCode = "IMPLAUSIBLE_PRICE"
	OutcomeCurrencyMismatch   OutcomeCode = "CURRENCY_MISMATCH"
	OutcomeFetchTransient     OutcomeCode = "FETCH_TRANSIENT"
	OutcomeRateLimited        OutcomeCode = "RATE_LIMITED"
	OutcomeBlocked            OutcomeCode = "BLOCKED"
	OutcomeRobotsDisallowed   OutcomeCode = "ROBOTS_DISALLOWED"
	OutcomeNotFound           OutcomeCode = "NOT_FOUND"
	OutcomeForbiddenTarget    OutcomeCode = "FORBIDDEN_TARGET"
	OutcomeUnsupportedContent OutcomeCode = "UNSUPPORTED_CONTENT"
	OutcomeTooLarge           OutcomeCode = "TOO_LARGE"
	OutcomeHTTPError          OutcomeCode = "HTTP_ERROR"
	OutcomeCircuitOpen        OutcomeCode = "CIRCUIT_OPEN"
	OutcomeProductInactive    OutcomeCode = "PRODUCT_INACTIVE"
)

// Retryable reports whether a later check might succeed without human action.
func (o OutcomeCode) Retryable() bool {
	switch o {
	case OutcomeFetchTransient, OutcomeRateLimited, OutcomeBlocked, OutcomeCircuitOpen, OutcomeHTTPError:
		return true
	}
	return false
}

// IsExtractionFailure reports whether the page loaded but no usable price was found.
// These indicate parser drift and count toward NEEDS_ATTENTION.
func (o OutcomeCode) IsExtractionFailure() bool {
	switch o {
	case OutcomePriceNotFound, OutcomeAmbiguousPrice, OutcomeImplausiblePrice, OutcomeCurrencyMismatch:
		return true
	}
	return false
}

// CountsAgainstRetailer reports whether this outcome signals the retailer is
// unhealthy or blocking us; such outcomes feed the per-domain circuit breaker.
func (o OutcomeCode) CountsAgainstRetailer() bool {
	switch o {
	case OutcomeFetchTransient, OutcomeRateLimited, OutcomeBlocked:
		return true
	}
	return false
}

// Check is one execution of a price check.
type Check struct {
	ID         string
	ProductID  string
	UserID     string
	Status     CheckStatus
	Trigger    Trigger
	QueuedAt   time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	DurationMS int64
	Outcome    OutcomeCode
	Message    string
	Price      *Money
	Strategy   string
	Alerted    bool
}

// CheckMessage is the queue payload asking a worker to check one product.
type CheckMessage struct {
	UserID     string    `json:"user_id"`
	ProductID  string    `json:"product_id"`
	CheckID    string    `json:"check_id"`
	Trigger    Trigger   `json:"trigger"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// NotificationStatus is the delivery state of an outbox notification.
type NotificationStatus string

const (
	NotificationPending NotificationStatus = "PENDING"
	NotificationSending NotificationStatus = "SENDING"
	NotificationSent    NotificationStatus = "SENT"
	NotificationFailed  NotificationStatus = "FAILED"
	NotificationSkipped NotificationStatus = "SKIPPED" // user disabled notifications
)

// Notification is a price alert waiting in (or delivered from) the outbox.
// It carries a snapshot of the product so delivery never needs another read.
type Notification struct {
	ID           string
	UserID       string
	ProductID    string
	ProductName  string
	ProductURL   string
	Price        Money
	Target       Money
	Status       NotificationStatus
	Attempts     int
	CreatedAt    time.Time
	SendingAt    *time.Time
	SentAt       *time.Time
	ProviderID   string
	ErrorMessage string
}

// Profile is per-user settings.
type Profile struct {
	UserID               string
	Email                string
	NotificationsEnabled bool
	ProductCount         int
	CreatedAt            time.Time
}

// DomainHealth is the shared circuit-breaker state for one retailer host.
type DomainHealth struct {
	Host                string
	ConsecutiveFailures int
	OpenUntil           *time.Time
	OpenCount           int // how many times in a row the circuit has opened; doubles the open window
	LastOutcome         OutcomeCode
	UpdatedAt           time.Time
}

// IsOpen reports whether checks against this host should be skipped right now.
func (h DomainHealth) IsOpen(now time.Time) bool {
	return h.OpenUntil != nil && now.Before(*h.OpenUntil)
}
