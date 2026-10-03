// Package notify delivers price alerts from the outbox. A notification is
// written in the same transaction that flips a product's alert state
// (ADR-004); Dispatch turns it into an email exactly when it can claim it.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// Email is a rendered message.
type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers email.
type Sender interface {
	Send(ctx context.Context, e Email) (providerMessageID string, err error)
}

// PermanentError marks failures that retrying cannot fix (invalid or
// suppressed address, rejected content).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return "permanent: " + e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Store is the persistence the dispatcher needs.
type Store interface {
	ClaimNotification(ctx context.Context, userID, id string, now time.Time, staleAfter time.Duration) (bool, error)
	GetNotification(ctx context.Context, userID, id string) (domain.Notification, error)
	CompleteNotification(ctx context.Context, userID, id string, status domain.NotificationStatus, providerID, errMsg string, now time.Time) error
	ReleaseNotification(ctx context.Context, userID, id, errMsg string) error
	GetProfile(ctx context.Context, userID string) (domain.Profile, error)
}

// Observer receives delivery results (metrics).
type Observer interface {
	NotificationDone(ctx context.Context, status domain.NotificationStatus)
}

// Dispatcher sends outbox notifications.
type Dispatcher struct {
	store    Store
	sender   Sender
	observer Observer
	baseURL  string
	log      *slog.Logger
	now      func() time.Time
	// StaleAfter lets a crashed sender's claim be retaken.
	StaleAfter time.Duration
}

// NewDispatcher builds a Dispatcher. baseURL is the web app's URL for links.
func NewDispatcher(s Store, sender Sender, observer Observer, baseURL string, log *slog.Logger) *Dispatcher {
	return &Dispatcher{store: s, sender: sender, observer: observer, baseURL: baseURL, log: log,
		now: func() time.Time { return time.Now().UTC() }, StaleAfter: 5 * time.Minute}
}

// Dispatch delivers one notification. It is idempotent: concurrent or
// repeated calls send at most one email except in the narrow window where
// the provider accepted a message and we crashed before recording it.
// A returned error means "retry later".
func (d *Dispatcher) Dispatch(ctx context.Context, userID, id string) error {
	ctx = telemetry.WithAttrs(ctx, slog.String("notification_id", id))
	claimed, err := d.store.ClaimNotification(ctx, userID, id, d.now(), d.StaleAfter)
	if err != nil {
		return err
	}
	if !claimed {
		d.log.InfoContext(ctx, "notification not claimable (already sent, being sent, or missing)")
		return nil
	}
	n, err := d.store.GetNotification(ctx, userID, id)
	if err != nil {
		return d.release(ctx, userID, id, err)
	}
	profile, err := d.store.GetProfile(ctx, userID)
	if err != nil {
		return d.release(ctx, userID, id, err)
	}
	if !profile.NotificationsEnabled || profile.Email == "" {
		reason := "notifications disabled"
		if profile.Email == "" {
			reason = "no email address on file"
		}
		d.done(ctx, domain.NotificationSkipped)
		return d.store.CompleteNotification(ctx, userID, id, domain.NotificationSkipped, "", reason, d.now())
	}

	msg, err := Render(n, profile.Email, d.baseURL)
	if err != nil {
		d.done(ctx, domain.NotificationFailed)
		return d.store.CompleteNotification(ctx, userID, id, domain.NotificationFailed, "", err.Error(), d.now())
	}
	providerID, err := d.sender.Send(ctx, msg)
	var perm *PermanentError
	switch {
	case errors.As(err, &perm):
		d.log.WarnContext(ctx, "notification permanently failed", "to", telemetry.Email(profile.Email), "err", err)
		d.done(ctx, domain.NotificationFailed)
		return d.store.CompleteNotification(ctx, userID, id, domain.NotificationFailed, "", err.Error(), d.now())
	case err != nil:
		return d.release(ctx, userID, id, err)
	}
	d.log.InfoContext(ctx, "notification sent", "to", telemetry.Email(profile.Email), "provider_id", providerID, "product_id", n.ProductID)
	d.done(ctx, domain.NotificationSent)
	return d.store.CompleteNotification(ctx, userID, id, domain.NotificationSent, providerID, "", d.now())
}

func (d *Dispatcher) release(ctx context.Context, userID, id string, cause error) error {
	d.log.WarnContext(ctx, "notification send failed; will retry", "err", cause)
	if err := d.store.ReleaseNotification(ctx, userID, id, cause.Error()); err != nil {
		return errors.Join(cause, err)
	}
	return fmt.Errorf("dispatch: %w", cause)
}

func (d *Dispatcher) done(ctx context.Context, s domain.NotificationStatus) {
	if d.observer != nil {
		d.observer.NotificationDone(ctx, s)
	}
}
