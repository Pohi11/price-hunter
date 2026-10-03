// Package products implements the API's use cases: managing tracked
// products, reading price history, and requesting checks. It enforces
// validation, ownership, quotas and cooldowns; storage details live in
// package store.
package products

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/retailer"
	"github.com/Pohi11/price-hunter/internal/store"
	"github.com/Pohi11/price-hunter/internal/urlx"
)

// Store is the persistence the service needs.
type Store interface {
	CreateProduct(ctx context.Context, p domain.Product, first domain.Check, maxProducts int) error
	GetProduct(ctx context.Context, userID, productID string) (domain.Product, error)
	ListProducts(ctx context.Context, userID string, limit int, cursor string) ([]domain.Product, string, error)
	UpdateProduct(ctx context.Context, userID, productID string, expectedVersion int, ch store.ProductChanges, now time.Time) (domain.Product, error)
	DeleteProduct(ctx context.Context, userID, productID string) (domain.Product, error)
	RequestManualCheck(ctx context.Context, userID, productID string, c domain.Check, cooldown time.Duration) error
	GetCheck(ctx context.Context, productID, checkID string) (domain.Check, error)
	ListChecks(ctx context.Context, productID string, limit int) ([]domain.Check, error)
	ListPrices(ctx context.Context, q store.PriceQuery) ([]domain.PricePoint, string, error)
	GetProfile(ctx context.Context, userID string) (domain.Profile, error)
	UpsertProfile(ctx context.Context, userID, email string, now time.Time) (domain.Profile, error)
	SetNotificationsEnabled(ctx context.Context, userID string, enabled bool) error
}

// Enqueuer sends check requests to the worker queue.
type Enqueuer interface {
	Enqueue(ctx context.Context, msgs ...domain.CheckMessage) error
}

// Config holds business limits.
type Config struct {
	MaxProducts     int           // per user
	ManualCooldown  time.Duration // between manual checks of one product
	FirstCheckLease time.Duration // scheduler waits this long before re-trying an unprocessed first check
	URLOptions      urlx.Options
}

// Service implements product use cases.
type Service struct {
	store    Store
	queue    Enqueuer
	registry *retailer.Registry
	cfg      Config
	log      *slog.Logger
	now      func() time.Time
	newID    func() string

	// OnDeleted, if set, runs after a product is deleted. Local mode uses it
	// to purge history inline; in AWS a DynamoDB Streams consumer does it.
	OnDeleted func(ctx context.Context, productID string)
}

// New builds a Service.
func New(s Store, q Enqueuer, reg *retailer.Registry, cfg Config, log *slog.Logger) *Service {
	if cfg.MaxProducts == 0 {
		cfg.MaxProducts = 50
	}
	if cfg.ManualCooldown == 0 {
		cfg.ManualCooldown = 5 * time.Minute
	}
	if cfg.FirstCheckLease == 0 {
		cfg.FirstCheckLease = 15 * time.Minute
	}
	return &Service{store: s, queue: q, registry: reg, cfg: cfg, log: log,
		now: func() time.Time { return time.Now().UTC() }, newID: func() string { return ulid.Make().String() }}
}

// SetClock replaces the clock and, if non-nil, the ID generator (tests).
func (s *Service) SetClock(now func() time.Time, newID func() string) {
	s.now = now
	if newID != nil {
		s.newID = newID
	}
}

// MoneyInput is a price as sent by clients: a decimal string plus currency.
type MoneyInput struct {
	Amount   string
	Currency string
}

// CreateInput is the request to track a product.
type CreateInput struct {
	Name      string
	URL       string
	Target    MoneyInput
	Frequency string
}

const maxNameLen = 120

// maxTargetMinor bounds targets at 10 million in a 2-decimal currency.
const maxTargetMinor = 1_000_000_000

// Create validates input, resolves the retailer, stores the product and
// queues its first check.
func (s *Service) Create(ctx context.Context, userID, email string, in CreateInput) (domain.Product, domain.Check, error) {
	verr := &domain.ValidationError{}
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		verr.Add("name", "REQUIRED", "name is required")
	case utf8.RuneCountInString(name) > maxNameLen:
		verr.Add("name", "TOO_LONG", "name must be at most 120 characters")
	}

	var u *url.URL
	var profile *retailer.Profile
	u, err := urlx.Validate(in.URL, s.cfg.URLOptions)
	if err != nil {
		var ue *urlx.Error
		if errors.As(err, &ue) {
			verr.Add("url", ue.Code, ue.Message)
		} else {
			verr.Add("url", "INVALID_URL", "URL is not valid")
		}
	} else {
		profile, err = s.registry.Lookup(u.Host)
		if err != nil {
			verr.Add("url", "UNSUPPORTED_RETAILER", "this retailer is not supported yet")
		}
	}

	currency := in.Target.Currency
	if currency == "" && profile != nil && profile.Currency != "" {
		currency = string(profile.Currency)
	}
	target, tErr := parseTarget(in.Target.Amount, currency)
	if tErr != nil {
		verr.Fields = append(verr.Fields, *tErr)
	} else if profile != nil && profile.Currency != "" && target.Currency != profile.Currency {
		verr.Add("target_price.currency", "CURRENCY_MISMATCH", "this retailer prices in "+string(profile.Currency))
	}

	freqStr := in.Frequency
	if freqStr == "" {
		freqStr = "6h"
	}
	freq, err := domain.ParseFrequency(freqStr)
	if err != nil {
		verr.Add("check_frequency", "INVALID", err.Error())
	}
	if err := verr.OrNil(); err != nil {
		return domain.Product{}, domain.Check{}, err
	}

	now := s.now()
	canonical := urlx.Canonicalize(u, profile.KeepParams)
	lease := now.Add(s.cfg.FirstCheckLease)
	p := domain.Product{
		ID: s.newID(), UserID: userID, Name: name,
		URL: urlx.StripTracking(u), CanonicalURL: canonical, URLHash: urlx.Hash(canonical),
		Retailer: profile.ID, Host: u.Hostname(),
		Target: target, Frequency: freq, Status: domain.StatusActive,
		AlertState: domain.AlertArmed, Availability: domain.AvailabilityUnknown,
		// The first check is enqueued directly below. next_check_at acts as a
		// lease: if that enqueue is lost, the scheduler picks the product up
		// when the lease expires.
		NextCheckAt: &lease,
		Version:     1, CreatedAt: now, UpdatedAt: now,
	}
	first := domain.Check{ID: s.newID(), ProductID: p.ID, UserID: userID, Status: domain.CheckQueued, Trigger: domain.TriggerInitial, QueuedAt: now}

	if email != "" {
		if _, err := s.store.UpsertProfile(ctx, userID, email, now); err != nil {
			return domain.Product{}, domain.Check{}, err
		}
	}
	if err := s.store.CreateProduct(ctx, p, first, s.cfg.MaxProducts); err != nil {
		return domain.Product{}, domain.Check{}, err
	}
	s.enqueue(ctx, first)
	return p, first, nil
}

func (s *Service) enqueue(ctx context.Context, c domain.Check) {
	msg := domain.CheckMessage{UserID: c.UserID, ProductID: c.ProductID, CheckID: c.ID, Trigger: c.Trigger, EnqueuedAt: c.QueuedAt}
	if err := s.queue.Enqueue(ctx, msg); err != nil {
		// Not fatal: next_check_at is already a lease, so the scheduler will
		// pick the product up when the lease expires.
		s.log.WarnContext(ctx, "enqueue failed; scheduler will retry", "product_id", c.ProductID, "check_id", c.ID, "err", err)
	}
}

func parseTarget(amount, currency string) (domain.Money, *domain.FieldError) {
	if strings.TrimSpace(amount) == "" {
		return domain.Money{}, &domain.FieldError{Field: "target_price.amount", Code: "REQUIRED", Message: "target price is required"}
	}
	if currency == "" {
		currency = "USD"
	}
	cur, err := domain.ParseCurrency(currency)
	if err != nil {
		return domain.Money{}, &domain.FieldError{Field: "target_price.currency", Code: "UNSUPPORTED_CURRENCY", Message: "unsupported currency"}
	}
	m, err := domain.ParseAmount(amount, cur)
	if err != nil {
		return domain.Money{}, &domain.FieldError{Field: "target_price.amount", Code: "INVALID", Message: "amount must be a decimal like \"99.99\""}
	}
	if m.Minor <= 0 || m.Minor > maxTargetMinor {
		return domain.Money{}, &domain.FieldError{Field: "target_price.amount", Code: "OUT_OF_RANGE", Message: "amount must be greater than 0 and less than 10,000,000"}
	}
	return m, nil
}

// Get returns a product with its most recent check.
func (s *Service) Get(ctx context.Context, userID, productID string) (domain.Product, *domain.Check, error) {
	p, err := s.store.GetProduct(ctx, userID, productID)
	if err != nil {
		return domain.Product{}, nil, err
	}
	checks, err := s.store.ListChecks(ctx, productID, 1)
	if err != nil {
		return domain.Product{}, nil, err
	}
	if len(checks) == 0 {
		return p, nil, nil
	}
	return p, &checks[0], nil
}

// List pages through the user's watchlist.
func (s *Service) List(ctx context.Context, userID string, limit int, cursor string) ([]domain.Product, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return s.store.ListProducts(ctx, userID, limit, cursor)
}

// UpdateInput holds optional edits; nil fields are unchanged.
type UpdateInput struct {
	Name      *string
	Target    *MoneyInput
	Frequency *string
	Paused    *bool
	IfMatch   int // expected version; 0 means "don't care"
}

// Update applies user edits with optimistic concurrency.
func (s *Service) Update(ctx context.Context, userID, productID string, in UpdateInput) (domain.Product, error) {
	p, err := s.store.GetProduct(ctx, userID, productID)
	if err != nil {
		return domain.Product{}, err
	}
	if in.IfMatch != 0 && in.IfMatch != p.Version {
		return domain.Product{}, domain.ErrVersionConflict
	}
	now := s.now()
	verr := &domain.ValidationError{}
	var ch store.ProductChanges

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		switch {
		case name == "":
			verr.Add("name", "REQUIRED", "name is required")
		case utf8.RuneCountInString(name) > maxNameLen:
			verr.Add("name", "TOO_LONG", "name must be at most 120 characters")
		default:
			ch.Name = &name
		}
	}
	if in.Target != nil {
		cur := in.Target.Currency
		if cur == "" {
			cur = string(p.Target.Currency)
		}
		t, fe := parseTarget(in.Target.Amount, cur)
		switch {
		case fe != nil:
			verr.Fields = append(verr.Fields, *fe)
		case t.Currency != p.Target.Currency:
			verr.Add("target_price.currency", "CURRENCY_MISMATCH", "currency cannot change; this product is priced in "+string(p.Target.Currency))
		case t != p.Target:
			ch.Target = &t
			armed := domain.AlertArmed // a new target deserves a fresh alert
			ch.AlertState = &armed
		}
	}
	status := p.Status
	next := p.NextCheckAt
	reschedule := false
	if in.Frequency != nil {
		f, err := domain.ParseFrequency(*in.Frequency)
		if err != nil {
			verr.Add("check_frequency", "INVALID", err.Error())
		} else if f != p.Frequency {
			ch.Frequency = &f
			if p.Status.Schedulable() {
				candidate := now
				if p.LastCheckedAt != nil {
					candidate = p.LastCheckedAt.Add(f.Duration())
				}
				if next == nil || candidate.Before(*next) {
					next = &candidate
				}
				reschedule = true
			}
		}
	}
	if in.Paused != nil {
		switch {
		case *in.Paused && status != domain.StatusPaused:
			status, next, reschedule = domain.StatusPaused, nil, true
		case !*in.Paused && (status == domain.StatusPaused || status == domain.StatusGone):
			status, next, reschedule = domain.StatusActive, &now, true
		}
		if status != p.Status {
			ch.Status = &status
		}
	}
	if err := verr.OrNil(); err != nil {
		return domain.Product{}, err
	}
	if reschedule {
		ch.Reschedule, ch.NextCheckAt, ch.ResultingStatus = true, next, status
	}
	return s.store.UpdateProduct(ctx, userID, productID, p.Version, ch, now)
}

// Delete removes a product. History is purged asynchronously.
func (s *Service) Delete(ctx context.Context, userID, productID string) error {
	p, err := s.store.DeleteProduct(ctx, userID, productID)
	if err != nil {
		return err
	}
	if s.OnDeleted != nil {
		s.OnDeleted(ctx, p.ID)
	}
	return nil
}

// TriggerCheck queues a manual check, subject to the per-product cooldown.
func (s *Service) TriggerCheck(ctx context.Context, userID, productID string) (domain.Check, error) {
	// Ownership: product-scoped partitions aren't keyed by user, so every
	// product-level read or write first proves the user owns the product.
	if _, err := s.store.GetProduct(ctx, userID, productID); err != nil {
		return domain.Check{}, err
	}
	c := domain.Check{ID: s.newID(), ProductID: productID, UserID: userID, Status: domain.CheckQueued, Trigger: domain.TriggerManual, QueuedAt: s.now()}
	if err := s.store.RequestManualCheck(ctx, userID, productID, c, s.cfg.ManualCooldown); err != nil {
		return domain.Check{}, err
	}
	msg := domain.CheckMessage{UserID: userID, ProductID: productID, CheckID: c.ID, Trigger: c.Trigger, EnqueuedAt: c.QueuedAt}
	if err := s.queue.Enqueue(ctx, msg); err != nil {
		return domain.Check{}, err
	}
	return c, nil
}

// GetCheck returns one check of a product the user owns.
func (s *Service) GetCheck(ctx context.Context, userID, productID, checkID string) (domain.Check, error) {
	if _, err := s.store.GetProduct(ctx, userID, productID); err != nil {
		return domain.Check{}, err
	}
	c, err := s.store.GetCheck(ctx, productID, checkID)
	if err != nil {
		return domain.Check{}, err
	}
	if c.UserID != "" && c.UserID != userID {
		return domain.Check{}, domain.ErrNotFound
	}
	return c, nil
}

// ListChecks returns recent checks of a product the user owns.
func (s *Service) ListChecks(ctx context.Context, userID, productID string, limit int) ([]domain.Check, error) {
	if _, err := s.store.GetProduct(ctx, userID, productID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	return s.store.ListChecks(ctx, productID, limit)
}

// Me returns the user's profile, recording their email on first sight.
func (s *Service) Me(ctx context.Context, userID, email string) (domain.Profile, error) {
	p, err := s.store.GetProfile(ctx, userID)
	if err != nil {
		return domain.Profile{}, err
	}
	if email != "" && p.Email != email {
		return s.store.UpsertProfile(ctx, userID, email, s.now())
	}
	return p, nil
}

// SetNotifications toggles alert emails.
func (s *Service) SetNotifications(ctx context.Context, userID, email string, enabled bool) (domain.Profile, error) {
	if _, err := s.Me(ctx, userID, email); err != nil {
		return domain.Profile{}, err
	}
	if err := s.store.SetNotificationsEnabled(ctx, userID, enabled); err != nil {
		return domain.Profile{}, err
	}
	return s.store.GetProfile(ctx, userID)
}
