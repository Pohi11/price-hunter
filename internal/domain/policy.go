package domain

import (
	"math/rand/v2"
	"time"
)

// AlertState is the per-product alert state machine.
//
//	ARMED --(price <= target, in stock)--> TRIGGERED (send one alert)
//	TRIGGERED --(price > target)--> ARMED
//	any --(user edits target)--> ARMED
type AlertState string

const (
	AlertArmed     AlertState = "ARMED"
	AlertTriggered AlertState = "TRIGGERED"
)

// EvaluateAlert returns the next alert state and whether an alert should fire.
func EvaluateAlert(state AlertState, target Money, price Money, avail Availability) (AlertState, bool) {
	if price.Currency != target.Currency || avail == OutOfStock {
		return state, false
	}
	if price.Minor <= target.Minor {
		if state != AlertTriggered {
			return AlertTriggered, true
		}
		return AlertTriggered, false
	}
	return AlertArmed, false
}

// Policy holds the tunables that decide scheduling, backoff and status changes.
type Policy struct {
	RecheckDelay       time.Duration // delay before confirming a suspicious price
	BaseBackoff        time.Duration // first retry delay after a failed check
	MaxBackoff         time.Duration // ceiling for retry delay; also the NEEDS_ATTENTION interval
	AttentionThreshold int           // consecutive extraction failures before NEEDS_ATTENTION
	GoneThreshold      int           // consecutive NOT_FOUND results before GONE
	AnomalyLow         float64       // new/old ratio below this is suspicious
	AnomalyHigh        float64       // new/old ratio above this is suspicious
	SuspectTolerance   float64       // relative difference that counts as "same price" on recheck
	JitterFraction     float64       // +/- spread applied to regular intervals
}

// DefaultPolicy returns production defaults.
func DefaultPolicy() Policy {
	return Policy{
		RecheckDelay:       15 * time.Minute,
		BaseBackoff:        15 * time.Minute,
		MaxBackoff:         24 * time.Hour,
		AttentionThreshold: 3,
		GoneThreshold:      3,
		AnomalyLow:         0.3,
		AnomalyHigh:        3.0,
		SuspectTolerance:   0.02,
		JitterFraction:     0.05,
	}
}

// Observation is a successfully extracted price.
type Observation struct {
	Price        Money
	Availability Availability
	Strategy     string
}

// CheckInput is everything the policy needs to know about one finished check.
type CheckInput struct {
	Outcome     OutcomeCode
	Observation *Observation // set only when Outcome == OutcomeOK
	Now         time.Time
	RetryAfter  time.Duration // retailer- or circuit-imposed minimum wait, if any
}

// Transition is the result of applying a check to a product.
type Transition struct {
	Product     Product     // product with updated tracking state
	Point       *PricePoint // price point to store, if any (ProductID/CheckID filled by caller)
	CheckStatus CheckStatus // final status for the check record
	Outcome     OutcomeCode // possibly refined outcome (e.g. currency mismatch)
	Alert       bool        // true exactly when an alert must be sent
	Suspect     bool        // the observed price was held back pending confirmation
}

// Jitter returns a random offset in [-d, +d]. Tests inject a deterministic one.
type Jitter func(d time.Duration) time.Duration

// RandomJitter is the production Jitter.
func RandomJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(2*d)+1)) - d
}

// Apply computes the product's new state after a check. It is pure: callers
// persist the returned Transition.
func (pol Policy) Apply(p Product, in CheckInput, jitter Jitter) Transition {
	if jitter == nil {
		jitter = RandomJitter
	}
	now := in.Now.UTC()
	t := Transition{Product: p, Outcome: in.Outcome}

	switch in.Outcome {
	case OutcomeProductInactive:
		t.CheckStatus = CheckSkipped
		return t
	case OutcomeCircuitOpen:
		next := now.Add(maxDur(in.RetryAfter, pol.BaseBackoff) + absDur(jitter(pol.BaseBackoff/4)))
		t.Product.NextCheckAt = &next
		t.CheckStatus = CheckSkipped
		return t
	}

	t.Product.LastCheckedAt = &now

	if in.Outcome == OutcomeOK && in.Observation != nil {
		obs := *in.Observation
		if obs.Price.Currency != p.Target.Currency {
			t.Outcome = OutcomeCurrencyMismatch
			return pol.applyFailure(t, now, in.RetryAfter, jitter)
		}
		return pol.applySuccess(t, obs, now, jitter)
	}
	return pol.applyFailure(t, now, in.RetryAfter, jitter)
}

func (pol Policy) applySuccess(t Transition, obs Observation, now time.Time, jitter Jitter) Transition {
	p := &t.Product
	t.CheckStatus = CheckSucceeded
	p.LastSuccessAt = &now
	p.ConsecutiveFailures = 0
	p.LastErrorCode = ""
	if p.Status == StatusNeedsAttention {
		p.Status = StatusActive
	}
	point := &PricePoint{ObservedAt: now, Price: obs.Price, Availability: obs.Availability, Strategy: obs.Strategy}
	t.Point = point

	if p.Current != nil && pol.anomalous(*p.Current, obs.Price) {
		confirmed := p.PendingSuspect != nil && pol.samePrice(*p.PendingSuspect, obs.Price)
		if !confirmed {
			// Hold the value back: record it as suspect, don't alert, recheck soon.
			price := obs.Price
			p.PendingSuspect = &price
			point.Suspect = true
			t.Suspect = true
			next := now.Add(pol.RecheckDelay)
			p.NextCheckAt = &next
			return t
		}
	}
	p.PendingSuspect = nil

	price := obs.Price
	p.Current = &price
	if p.Lowest == nil || price.Minor < p.Lowest.Minor {
		lowest := price
		p.Lowest = &lowest
	}
	p.Availability = obs.Availability

	state := p.AlertState
	if state == "" {
		state = AlertArmed
	}
	newState, fire := EvaluateAlert(state, p.Target, price, obs.Availability)
	p.AlertState = newState
	if fire {
		t.Alert = true
		p.LastAlertAt = &now
	}

	next := now.Add(p.Frequency.Duration() + jitter(time.Duration(float64(p.Frequency.Duration())*pol.JitterFraction)))
	p.NextCheckAt = &next
	return t
}

func (pol Policy) applyFailure(t Transition, now time.Time, retryAfter time.Duration, jitter Jitter) Transition {
	p := &t.Product
	t.CheckStatus = CheckFailed
	p.ConsecutiveFailures++
	p.LastErrorCode = t.Outcome

	var delay time.Duration
	switch {
	case t.Outcome == OutcomeNotFound && p.ConsecutiveFailures >= pol.GoneThreshold:
		p.Status = StatusGone
		p.NextCheckAt = nil // unscheduled
		return t
	case t.Outcome.IsExtractionFailure() && p.ConsecutiveFailures >= pol.AttentionThreshold,
		t.Outcome == OutcomeForbiddenTarget, t.Outcome == OutcomeRobotsDisallowed,
		t.Outcome == OutcomeUnsupportedContent, t.Outcome == OutcomeTooLarge:
		p.Status = StatusNeedsAttention
		delay = pol.MaxBackoff
	default:
		delay = pol.Backoff(p.ConsecutiveFailures)
	}
	delay = maxDur(delay, retryAfter)
	next := now.Add(delay + absDur(jitter(delay/10)))
	p.NextCheckAt = &next
	return t
}

// Backoff returns the exponential retry delay after n consecutive failures.
func (pol Policy) Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := pol.BaseBackoff
	for i := 1; i < n; i++ {
		d *= 2
		if d >= pol.MaxBackoff {
			return pol.MaxBackoff
		}
	}
	return d
}

func (pol Policy) anomalous(prev, cur Money) bool {
	if prev.Minor <= 0 || prev.Currency != cur.Currency {
		return false
	}
	ratio := float64(cur.Minor) / float64(prev.Minor)
	return ratio < pol.AnomalyLow || ratio > pol.AnomalyHigh
}

func (pol Policy) samePrice(a, b Money) bool {
	if a.Currency != b.Currency || a.Minor <= 0 {
		return false
	}
	diff := float64(a.Minor-b.Minor) / float64(a.Minor)
	if diff < 0 {
		diff = -diff
	}
	return diff <= pol.SuspectTolerance
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
