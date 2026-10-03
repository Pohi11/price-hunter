package api

import (
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/products"
)

// Wire types. Money is a decimal string plus currency, never a float.

type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func toMoney(m domain.Money) moneyDTO {
	return moneyDTO{Amount: m.Decimal(), Currency: string(m.Currency)}
}

func toMoneyPtr(m *domain.Money) *moneyDTO {
	if m == nil {
		return nil
	}
	d := toMoney(*m)
	return &d
}

type alertDTO struct {
	State       string     `json:"state"`
	TriggeredAt *time.Time `json:"triggered_at"`
}

type lastErrorDTO struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

type checkSummaryDTO struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type productDTO struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	URL            string           `json:"url"`
	Retailer       string           `json:"retailer"`
	TargetPrice    moneyDTO         `json:"target_price"`
	CurrentPrice   *moneyDTO        `json:"current_price"`
	LowestPrice    *moneyDTO        `json:"lowest_price"`
	Availability   string           `json:"availability"`
	CheckFrequency string           `json:"check_frequency"`
	Status         string           `json:"status"`
	Stale          bool             `json:"stale"`
	Alert          alertDTO         `json:"alert"`
	LastCheckedAt  *time.Time       `json:"last_checked_at"`
	LastSuccessAt  *time.Time       `json:"last_success_at"`
	NextCheckAt    *time.Time       `json:"next_check_at"`
	LastError      *lastErrorDTO    `json:"last_error"`
	LatestCheck    *checkSummaryDTO `json:"latest_check,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	Version        int              `json:"version"`
}

func toProduct(p domain.Product, latest *domain.Check, now time.Time) productDTO {
	d := productDTO{
		ID: p.ID, Name: p.Name, URL: p.URL, Retailer: p.Retailer,
		TargetPrice: toMoney(p.Target), CurrentPrice: toMoneyPtr(p.Current), LowestPrice: toMoneyPtr(p.Lowest),
		Availability: string(p.Availability), CheckFrequency: p.Frequency.String(), Status: string(p.Status),
		Stale: p.IsStale(now), Alert: alertDTO{State: string(p.AlertState)},
		LastCheckedAt: p.LastCheckedAt, LastSuccessAt: p.LastSuccessAt, NextCheckAt: p.NextCheckAt,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Version: p.Version,
	}
	if d.Availability == "" {
		d.Availability = string(domain.AvailabilityUnknown)
	}
	if p.AlertState == domain.AlertTriggered {
		d.Alert.TriggeredAt = p.LastAlertAt
	}
	if p.LastErrorCode != "" {
		d.LastError = &lastErrorDTO{Code: string(p.LastErrorCode), Retryable: p.LastErrorCode.Retryable()}
	}
	if latest != nil {
		d.LatestCheck = &checkSummaryDTO{ID: latest.ID, Status: string(latest.Status)}
	}
	return d
}

type listDTO[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type createProductRequest struct {
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	TargetPrice    *moneyDTO `json:"target_price"`
	CheckFrequency string    `json:"check_frequency"`
}

type updateProductRequest struct {
	Name           *string   `json:"name"`
	TargetPrice    *moneyDTO `json:"target_price"`
	CheckFrequency *string   `json:"check_frequency"`
	Paused         *bool     `json:"paused"`
}

func (m *moneyDTO) input() products.MoneyInput {
	if m == nil {
		return products.MoneyInput{}
	}
	return products.MoneyInput{Amount: m.Amount, Currency: m.Currency}
}

type pointDTO struct {
	T            time.Time `json:"t"`
	Price        string    `json:"price"`
	Availability string    `json:"availability"`
	Suspect      bool      `json:"suspect,omitempty"`
}

type summaryDTO struct {
	Min    *string `json:"min"`
	Max    *string `json:"max"`
	Latest *string `json:"latest"`
	Count  int     `json:"count"`
}

type historyDTO struct {
	ProductID  string     `json:"product_id"`
	Currency   string     `json:"currency"`
	Resolution string     `json:"resolution"`
	Points     []pointDTO `json:"points"`
	Summary    summaryDTO `json:"summary"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

func decimalPtr(m *domain.Money) *string {
	if m == nil {
		return nil
	}
	s := m.Decimal()
	return &s
}

func toHistory(productID string, h products.History) historyDTO {
	d := historyDTO{ProductID: productID, Currency: string(h.Currency), Resolution: h.Resolution,
		Points: make([]pointDTO, len(h.Points)), NextCursor: h.NextCursor}
	for i, p := range h.Points {
		d.Points[i] = pointDTO{T: p.ObservedAt, Price: p.Price.Decimal(), Availability: string(p.Availability), Suspect: p.Suspect}
	}
	d.Summary = summaryDTO{Min: decimalPtr(h.Min), Max: decimalPtr(h.Max), Count: h.Count}
	if h.Latest != nil {
		d.Summary.Latest = decimalPtr(&h.Latest.Price)
	}
	return d
}

type checkResultDTO struct {
	Price          *moneyDTO `json:"price"`
	Strategy       string    `json:"strategy,omitempty"`
	AlertTriggered bool      `json:"alert_triggered"`
}

type checkErrorDTO struct {
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable"`
}

type checkDTO struct {
	CheckID    string            `json:"check_id"`
	ProductID  string            `json:"product_id"`
	Status     string            `json:"status"`
	Trigger    string            `json:"trigger"`
	QueuedAt   time.Time         `json:"queued_at"`
	StartedAt  *time.Time        `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	DurationMS *int64            `json:"duration_ms"`
	Result     *checkResultDTO   `json:"result"`
	Error      *checkErrorDTO    `json:"error"`
	Links      map[string]string `json:"links"`
}

func toCheck(c domain.Check) checkDTO {
	d := checkDTO{
		CheckID: c.ID, ProductID: c.ProductID, Status: string(c.Status), Trigger: string(c.Trigger),
		QueuedAt: c.QueuedAt, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt,
		Links: map[string]string{"self": "/v1/products/" + c.ProductID + "/checks/" + c.ID},
	}
	if c.Status.Terminal() {
		ms := c.DurationMS
		d.DurationMS = &ms
	}
	switch c.Status {
	case domain.CheckSucceeded:
		d.Result = &checkResultDTO{Price: toMoneyPtr(c.Price), Strategy: c.Strategy, AlertTriggered: c.Alerted}
	case domain.CheckFailed, domain.CheckSkipped:
		d.Error = &checkErrorDTO{Code: string(c.Outcome), Message: outcomeMessage(c.Outcome), Retryable: c.Outcome.Retryable()}
	}
	return d
}

// outcomeMessage gives users a stable, non-leaky explanation per outcome.
func outcomeMessage(o domain.OutcomeCode) string {
	switch o {
	case domain.OutcomePriceNotFound:
		return "No price was found on the page."
	case domain.OutcomeAmbiguousPrice:
		return "The page shows more than one price."
	case domain.OutcomeImplausiblePrice:
		return "The price on the page looks wrong."
	case domain.OutcomeCurrencyMismatch:
		return "The page uses a different currency than your target."
	case domain.OutcomeFetchTransient:
		return "The retailer did not respond. We'll retry."
	case domain.OutcomeRateLimited:
		return "The retailer asked us to slow down. We'll retry later."
	case domain.OutcomeBlocked:
		return "The retailer blocked the request."
	case domain.OutcomeRobotsDisallowed:
		return "The retailer's robots.txt disallows automated access to this page."
	case domain.OutcomeNotFound:
		return "The product page no longer exists."
	case domain.OutcomeForbiddenTarget:
		return "This address is not allowed."
	case domain.OutcomeUnsupportedContent, domain.OutcomeTooLarge:
		return "The page could not be read."
	case domain.OutcomeCircuitOpen:
		return "The retailer is having problems; checks are paused briefly."
	case domain.OutcomeProductInactive:
		return "The product is paused or no longer tracked."
	}
	return ""
}

type profileDTO struct {
	UserID               string `json:"user_id"`
	Email                string `json:"email"`
	NotificationsEnabled bool   `json:"notifications_enabled"`
	ProductCount         int    `json:"product_count"`
	ProductLimit         int    `json:"product_limit"`
}

type updateMeRequest struct {
	NotificationsEnabled *bool `json:"notifications_enabled"`
}

type retailerDTO struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Domains  []string `json:"domains"`
	Currency string   `json:"currency,omitempty"`
}

type retailersDTO struct {
	Items          []retailerDTO `json:"items"`
	GenericAllowed bool          `json:"generic_allowed"`
}
