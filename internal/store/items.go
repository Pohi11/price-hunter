package store

import (
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// Entity discriminators, stored in the "entity" attribute. Stream consumers
// filter on these.
const (
	EntityProduct      = "Product"
	EntityURLGuard     = "URLGuard"
	EntityProfile      = "Profile"
	EntityPricePoint   = "PricePoint"
	EntityCheck        = "Check"
	EntityNotification = "Notification"
	EntityDomainHealth = "DomainHealth"
)

// Retention for operational records.
const (
	checkTTL        = 30 * 24 * time.Hour
	notificationTTL = 90 * 24 * time.Hour
)

type productItem struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	Entity string `dynamodbav:"entity"`
	GSI1PK string `dynamodbav:"GSI1PK,omitempty"`
	GSI1SK string `dynamodbav:"GSI1SK,omitempty"`

	ProductID    string `dynamodbav:"product_id"`
	UserID       string `dynamodbav:"user_id"`
	Name         string `dynamodbav:"name"`
	URL          string `dynamodbav:"url"`
	CanonicalURL string `dynamodbav:"canonical_url"`
	URLHash      string `dynamodbav:"url_hash"`
	Retailer     string `dynamodbav:"retailer"`
	Host         string `dynamodbav:"host"`
	TargetMinor  int64  `dynamodbav:"target_minor"`
	Currency     string `dynamodbav:"currency"`
	FrequencyMin int    `dynamodbav:"frequency_min"`
	Status       string `dynamodbav:"status"`

	CurrentMinor        *int64  `dynamodbav:"current_minor,omitempty"`
	LowestMinor         *int64  `dynamodbav:"lowest_minor,omitempty"`
	PendingSuspectMinor *int64  `dynamodbav:"pending_suspect_minor,omitempty"`
	Availability        string  `dynamodbav:"availability,omitempty"`
	AlertState          string  `dynamodbav:"alert_state"`
	LastAlertAt         *string `dynamodbav:"last_alert_at,omitempty"`
	LastCheckedAt       *string `dynamodbav:"last_checked_at,omitempty"`
	LastSuccessAt       *string `dynamodbav:"last_success_at,omitempty"`
	LastErrorCode       string  `dynamodbav:"last_error_code,omitempty"`
	ConsecutiveFailures int     `dynamodbav:"consecutive_failures"`
	NextCheckAt         *string `dynamodbav:"next_check_at,omitempty"`
	LastManualCheckAt   *string `dynamodbav:"last_manual_check_at,omitempty"`

	Version   int    `dynamodbav:"version"`
	CreatedAt string `dynamodbav:"created_at"`
	UpdatedAt string `dynamodbav:"updated_at"`
}

func moneyPtr(minor *int64, cur domain.Currency) *domain.Money {
	if minor == nil {
		return nil
	}
	return &domain.Money{Minor: *minor, Currency: cur}
}

func minorPtr(m *domain.Money) *int64 {
	if m == nil {
		return nil
	}
	v := m.Minor
	return &v
}

func toProductItem(p domain.Product) productItem {
	it := productItem{
		PK: userPK(p.UserID), SK: productSK(p.ID), Entity: EntityProduct,
		ProductID: p.ID, UserID: p.UserID, Name: p.Name, URL: p.URL, CanonicalURL: p.CanonicalURL,
		URLHash: p.URLHash, Retailer: p.Retailer, Host: p.Host,
		TargetMinor: p.Target.Minor, Currency: string(p.Target.Currency),
		FrequencyMin: int(p.Frequency.Duration() / time.Minute), Status: string(p.Status),
		CurrentMinor: minorPtr(p.Current), LowestMinor: minorPtr(p.Lowest), PendingSuspectMinor: minorPtr(p.PendingSuspect),
		Availability: string(p.Availability), AlertState: string(p.AlertState),
		LastAlertAt: tsPtr(p.LastAlertAt), LastCheckedAt: tsPtr(p.LastCheckedAt), LastSuccessAt: tsPtr(p.LastSuccessAt),
		LastErrorCode: string(p.LastErrorCode), ConsecutiveFailures: p.ConsecutiveFailures,
		NextCheckAt: tsPtr(p.NextCheckAt), LastManualCheckAt: tsPtr(p.LastManualCheckAt),
		Version: p.Version, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt),
	}
	if p.Status.Schedulable() && p.NextCheckAt != nil {
		it.GSI1PK = dueShardPK(ShardFor(p.ID))
		it.GSI1SK = ts(*p.NextCheckAt)
	}
	return it
}

func (it productItem) toDomain() domain.Product {
	cur := domain.Currency(it.Currency)
	created, _ := parseTS(it.CreatedAt)
	updated, _ := parseTS(it.UpdatedAt)
	return domain.Product{
		ID: it.ProductID, UserID: it.UserID, Name: it.Name, URL: it.URL, CanonicalURL: it.CanonicalURL,
		URLHash: it.URLHash, Retailer: it.Retailer, Host: it.Host,
		Target:    domain.Money{Minor: it.TargetMinor, Currency: cur},
		Frequency: domain.Frequency(time.Duration(it.FrequencyMin) * time.Minute),
		Status:    domain.ProductStatus(it.Status),
		Current:   moneyPtr(it.CurrentMinor, cur), Lowest: moneyPtr(it.LowestMinor, cur),
		PendingSuspect: moneyPtr(it.PendingSuspectMinor, cur),
		Availability:   domain.Availability(it.Availability), AlertState: domain.AlertState(it.AlertState),
		LastAlertAt: parseTSPtr(it.LastAlertAt), LastCheckedAt: parseTSPtr(it.LastCheckedAt),
		LastSuccessAt: parseTSPtr(it.LastSuccessAt), LastErrorCode: domain.OutcomeCode(it.LastErrorCode),
		ConsecutiveFailures: it.ConsecutiveFailures, NextCheckAt: parseTSPtr(it.NextCheckAt),
		LastManualCheckAt: parseTSPtr(it.LastManualCheckAt),
		Version:           it.Version, CreatedAt: created, UpdatedAt: updated,
	}
}

type urlGuardItem struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	Entity    string `dynamodbav:"entity"`
	ProductID string `dynamodbav:"product_id"`
}

type profileItem struct {
	PK                   string `dynamodbav:"PK"`
	SK                   string `dynamodbav:"SK"`
	Entity               string `dynamodbav:"entity"`
	UserID               string `dynamodbav:"user_id"`
	Email                string `dynamodbav:"email"`
	NotificationsEnabled bool   `dynamodbav:"notifications_enabled"`
	ProductCount         int    `dynamodbav:"product_count"`
	CreatedAt            string `dynamodbav:"created_at"`
}

func (it profileItem) toDomain() domain.Profile {
	created, _ := parseTS(it.CreatedAt)
	return domain.Profile{UserID: it.UserID, Email: it.Email, NotificationsEnabled: it.NotificationsEnabled,
		ProductCount: it.ProductCount, CreatedAt: created}
}

type pricePointItem struct {
	PK           string `dynamodbav:"PK"`
	SK           string `dynamodbav:"SK"`
	Entity       string `dynamodbav:"entity"`
	ProductID    string `dynamodbav:"product_id"`
	ObservedAt   string `dynamodbav:"observed_at"`
	AmountMinor  int64  `dynamodbav:"amount_minor"`
	Currency     string `dynamodbav:"currency"`
	Availability string `dynamodbav:"availability"`
	Strategy     string `dynamodbav:"strategy,omitempty"`
	CheckID      string `dynamodbav:"check_id,omitempty"`
	Suspect      bool   `dynamodbav:"suspect,omitempty"`
}

func toPricePointItem(p domain.PricePoint) pricePointItem {
	return pricePointItem{
		PK: productPK(p.ProductID), SK: priceSK(p.ObservedAt), Entity: EntityPricePoint,
		ProductID: p.ProductID, ObservedAt: ts(p.ObservedAt), AmountMinor: p.Price.Minor,
		Currency: string(p.Price.Currency), Availability: string(p.Availability),
		Strategy: p.Strategy, CheckID: p.CheckID, Suspect: p.Suspect,
	}
}

func (it pricePointItem) toDomain() domain.PricePoint {
	t, _ := parseTS(it.ObservedAt)
	return domain.PricePoint{
		ProductID: it.ProductID, ObservedAt: t,
		Price:        domain.Money{Minor: it.AmountMinor, Currency: domain.Currency(it.Currency)},
		Availability: domain.Availability(it.Availability), Strategy: it.Strategy, CheckID: it.CheckID, Suspect: it.Suspect,
	}
}

type checkItem struct {
	PK         string  `dynamodbav:"PK"`
	SK         string  `dynamodbav:"SK"`
	Entity     string  `dynamodbav:"entity"`
	CheckID    string  `dynamodbav:"check_id"`
	ProductID  string  `dynamodbav:"product_id"`
	UserID     string  `dynamodbav:"user_id"`
	Status     string  `dynamodbav:"status"`
	Trigger    string  `dynamodbav:"trigger"`
	QueuedAt   string  `dynamodbav:"queued_at"`
	StartedAt  *string `dynamodbav:"started_at,omitempty"`
	FinishedAt *string `dynamodbav:"finished_at,omitempty"`
	DurationMS int64   `dynamodbav:"duration_ms,omitempty"`
	Outcome    string  `dynamodbav:"outcome,omitempty"`
	Message    string  `dynamodbav:"message,omitempty"`
	PriceMinor *int64  `dynamodbav:"price_minor,omitempty"`
	Currency   string  `dynamodbav:"currency,omitempty"`
	Strategy   string  `dynamodbav:"strategy,omitempty"`
	Alerted    bool    `dynamodbav:"alerted,omitempty"`
	TTL        int64   `dynamodbav:"ttl"`
}

func (it checkItem) toDomain() domain.Check {
	queued, _ := parseTS(it.QueuedAt)
	return domain.Check{
		ID: it.CheckID, ProductID: it.ProductID, UserID: it.UserID,
		Status: domain.CheckStatus(it.Status), Trigger: domain.Trigger(it.Trigger),
		QueuedAt: queued, StartedAt: parseTSPtr(it.StartedAt), FinishedAt: parseTSPtr(it.FinishedAt),
		DurationMS: it.DurationMS, Outcome: domain.OutcomeCode(it.Outcome), Message: it.Message,
		Price: moneyPtr(it.PriceMinor, domain.Currency(it.Currency)), Strategy: it.Strategy, Alerted: it.Alerted,
	}
}

type notificationItem struct {
	PK           string  `dynamodbav:"PK"`
	SK           string  `dynamodbav:"SK"`
	Entity       string  `dynamodbav:"entity"`
	NotifID      string  `dynamodbav:"notification_id"`
	UserID       string  `dynamodbav:"user_id"`
	ProductID    string  `dynamodbav:"product_id"`
	ProductName  string  `dynamodbav:"product_name"`
	ProductURL   string  `dynamodbav:"product_url"`
	PriceMinor   int64   `dynamodbav:"price_minor"`
	TargetMinor  int64   `dynamodbav:"target_minor"`
	Currency     string  `dynamodbav:"currency"`
	Status       string  `dynamodbav:"status"`
	Attempts     int     `dynamodbav:"attempts"`
	CreatedAt    string  `dynamodbav:"created_at"`
	SendingAt    *string `dynamodbav:"sending_at,omitempty"`
	SentAt       *string `dynamodbav:"sent_at,omitempty"`
	ProviderID   string  `dynamodbav:"provider_id,omitempty"`
	ErrorMessage string  `dynamodbav:"error_message,omitempty"`
	TTL          int64   `dynamodbav:"ttl"`
}

func toNotificationItem(n domain.Notification) notificationItem {
	return notificationItem{
		PK: userPK(n.UserID), SK: notifSK(n.ID), Entity: EntityNotification, NotifID: n.ID, UserID: n.UserID,
		ProductID: n.ProductID, ProductName: n.ProductName, ProductURL: n.ProductURL,
		PriceMinor: n.Price.Minor, TargetMinor: n.Target.Minor, Currency: string(n.Price.Currency),
		Status: string(n.Status), Attempts: n.Attempts, CreatedAt: ts(n.CreatedAt),
		TTL: ttlAt(n.CreatedAt.Add(notificationTTL)),
	}
}

func (it notificationItem) toDomain() domain.Notification {
	created, _ := parseTS(it.CreatedAt)
	cur := domain.Currency(it.Currency)
	return domain.Notification{
		ID: it.NotifID, UserID: it.UserID, ProductID: it.ProductID, ProductName: it.ProductName, ProductURL: it.ProductURL,
		Price: domain.Money{Minor: it.PriceMinor, Currency: cur}, Target: domain.Money{Minor: it.TargetMinor, Currency: cur},
		Status: domain.NotificationStatus(it.Status), Attempts: it.Attempts, CreatedAt: created,
		SendingAt: parseTSPtr(it.SendingAt), SentAt: parseTSPtr(it.SentAt), ProviderID: it.ProviderID, ErrorMessage: it.ErrorMessage,
	}
}

type domainHealthItem struct {
	PK                  string  `dynamodbav:"PK"`
	SK                  string  `dynamodbav:"SK"`
	Entity              string  `dynamodbav:"entity"`
	Host                string  `dynamodbav:"host"`
	ConsecutiveFailures int     `dynamodbav:"consecutive_failures"`
	OpenUntil           *string `dynamodbav:"open_until,omitempty"`
	OpenCount           int     `dynamodbav:"open_count"`
	LastOutcome         string  `dynamodbav:"last_outcome,omitempty"`
	UpdatedAt           string  `dynamodbav:"updated_at"`
}

func (it domainHealthItem) toDomain() domain.DomainHealth {
	updated, _ := parseTS(it.UpdatedAt)
	return domain.DomainHealth{Host: it.Host, ConsecutiveFailures: it.ConsecutiveFailures,
		OpenUntil: parseTSPtr(it.OpenUntil), OpenCount: it.OpenCount,
		LastOutcome: domain.OutcomeCode(it.LastOutcome), UpdatedAt: updated}
}
