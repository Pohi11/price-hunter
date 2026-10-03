package domain

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func noJitter(time.Duration) time.Duration { return 0 }

func usd(minor int64) Money { return Money{Minor: minor, Currency: "USD"} }

func ptr[T any](v T) *T { return &v }

func baseProduct() Product {
	return Product{
		ID: "p1", UserID: "u1", Name: "Ninja Air Fryer",
		Target: usd(10000), Frequency: Frequency(6 * time.Hour),
		Status: StatusActive, AlertState: AlertArmed,
	}
}

func ok(minor int64, avail Availability) CheckInput {
	return CheckInput{
		Outcome:     OutcomeOK,
		Observation: &Observation{Price: usd(minor), Availability: avail, Strategy: "jsonld"},
		Now:         t0,
	}
}

func TestEvaluateAlert(t *testing.T) {
	tests := []struct {
		name      string
		state     AlertState
		price     int64
		avail     Availability
		wantState AlertState
		wantFire  bool
	}{
		{"armed and below target fires", AlertArmed, 9400, InStock, AlertTriggered, true},
		{"armed and equal to target fires", AlertArmed, 10000, InStock, AlertTriggered, true},
		{"armed and above target stays armed", AlertArmed, 10001, InStock, AlertArmed, false},
		{"triggered and still below does not refire", AlertTriggered, 9000, InStock, AlertTriggered, false},
		{"triggered and back above re-arms", AlertTriggered, 12000, InStock, AlertArmed, false},
		{"out of stock never fires", AlertArmed, 5000, OutOfStock, AlertArmed, false},
		{"unknown availability may fire", AlertArmed, 5000, AvailabilityUnknown, AlertTriggered, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, fire := EvaluateAlert(tt.state, usd(10000), usd(tt.price), tt.avail)
			if s != tt.wantState || fire != tt.wantFire {
				t.Fatalf("got (%s, %v), want (%s, %v)", s, fire, tt.wantState, tt.wantFire)
			}
		})
	}
	if _, fire := EvaluateAlert(AlertArmed, usd(10000), Money{5000, "EUR"}, InStock); fire {
		t.Fatal("currency mismatch must not fire")
	}
}

func TestApplySuccessFiresOnceAndSchedules(t *testing.T) {
	pol := DefaultPolicy()
	p := baseProduct()

	tr := pol.Apply(p, ok(9400, InStock), noJitter)
	if !tr.Alert || tr.CheckStatus != CheckSucceeded {
		t.Fatalf("first crossing: alert=%v status=%s", tr.Alert, tr.CheckStatus)
	}
	if tr.Product.Current.Minor != 9400 || tr.Product.Lowest.Minor != 9400 {
		t.Fatalf("current/lowest not updated: %+v %+v", tr.Product.Current, tr.Product.Lowest)
	}
	if want := t0.Add(6 * time.Hour); !tr.Product.NextCheckAt.Equal(want) {
		t.Fatalf("next check = %v, want %v", tr.Product.NextCheckAt, want)
	}
	if tr.Point == nil || tr.Point.Suspect {
		t.Fatal("expected a normal price point")
	}

	// Second check still below target: no duplicate alert.
	tr2 := pol.Apply(tr.Product, ok(9300, InStock), noJitter)
	if tr2.Alert {
		t.Fatal("alert fired twice for the same crossing")
	}
	if tr2.Product.Lowest.Minor != 9300 {
		t.Fatalf("lowest = %d, want 9300", tr2.Product.Lowest.Minor)
	}
}

func TestApplyAnomalyGate(t *testing.T) {
	pol := DefaultPolicy()
	p := baseProduct()
	p.Current = ptr(usd(12999))

	// 12999 -> 999 is a >70% drop: held back as suspect, no alert.
	tr := pol.Apply(p, ok(999, InStock), noJitter)
	if tr.Alert || !tr.Suspect || !tr.Point.Suspect {
		t.Fatalf("expected suspect without alert, got alert=%v suspect=%v", tr.Alert, tr.Suspect)
	}
	if tr.Product.Current.Minor != 12999 {
		t.Fatal("suspect price must not replace current price")
	}
	if want := t0.Add(pol.RecheckDelay); !tr.Product.NextCheckAt.Equal(want) {
		t.Fatalf("recheck at %v, want %v", tr.Product.NextCheckAt, want)
	}

	// Recheck sees the same price: confirmed, accepted, alert fires.
	in := ok(1005, InStock)
	in.Now = t0.Add(15 * time.Minute)
	tr2 := pol.Apply(tr.Product, in, noJitter)
	if !tr2.Alert || tr2.Suspect || tr2.Product.Current.Minor != 1005 || tr2.Product.PendingSuspect != nil {
		t.Fatalf("confirmation failed: alert=%v suspect=%v current=%v", tr2.Alert, tr2.Suspect, tr2.Product.Current)
	}

	// Recheck sees a normal price again: the suspect value is discarded.
	tr3 := pol.Apply(tr.Product, ok(12899, InStock), noJitter)
	if tr3.Suspect || tr3.Product.PendingSuspect != nil || tr3.Product.Current.Minor != 12899 {
		t.Fatalf("expected suspect cleared, got %+v", tr3.Product)
	}
}

func TestApplyFailures(t *testing.T) {
	pol := DefaultPolicy()

	t.Run("transient failures back off exponentially", func(t *testing.T) {
		p := baseProduct()
		wantDelays := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour}
		for i, want := range wantDelays {
			tr := pol.Apply(p, CheckInput{Outcome: OutcomeFetchTransient, Now: t0}, noJitter)
			if tr.CheckStatus != CheckFailed || tr.Product.ConsecutiveFailures != i+1 {
				t.Fatalf("attempt %d: status=%s failures=%d", i+1, tr.CheckStatus, tr.Product.ConsecutiveFailures)
			}
			if got := tr.Product.NextCheckAt.Sub(t0); got != want {
				t.Fatalf("attempt %d: delay %v, want %v", i+1, got, want)
			}
			if tr.Product.Status != StatusActive {
				t.Fatalf("transient failures must not change status, got %s", tr.Product.Status)
			}
			p = tr.Product
		}
	})

	t.Run("retry-after is honored", func(t *testing.T) {
		tr := pol.Apply(baseProduct(), CheckInput{Outcome: OutcomeRateLimited, Now: t0, RetryAfter: 2 * time.Hour}, noJitter)
		if got := tr.Product.NextCheckAt.Sub(t0); got != 2*time.Hour {
			t.Fatalf("delay %v, want 2h", got)
		}
	})

	t.Run("extraction failures lead to NEEDS_ATTENTION", func(t *testing.T) {
		p := baseProduct()
		for i := 0; i < pol.AttentionThreshold; i++ {
			p = pol.Apply(p, CheckInput{Outcome: OutcomePriceNotFound, Now: t0}, noJitter).Product
		}
		if p.Status != StatusNeedsAttention {
			t.Fatalf("status %s, want NEEDS_ATTENTION", p.Status)
		}
		if got := p.NextCheckAt.Sub(t0); got != pol.MaxBackoff {
			t.Fatalf("delay %v, want %v", got, pol.MaxBackoff)
		}
		// A later success restores ACTIVE.
		tr := pol.Apply(p, ok(11000, InStock), noJitter)
		if tr.Product.Status != StatusActive || tr.Product.ConsecutiveFailures != 0 {
			t.Fatalf("recovery failed: %s / %d", tr.Product.Status, tr.Product.ConsecutiveFailures)
		}
	})

	t.Run("not found three times marks GONE and unschedules", func(t *testing.T) {
		p := baseProduct()
		for i := 0; i < pol.GoneThreshold; i++ {
			p = pol.Apply(p, CheckInput{Outcome: OutcomeNotFound, Now: t0}, noJitter).Product
		}
		if p.Status != StatusGone || p.NextCheckAt != nil {
			t.Fatalf("status %s next %v, want GONE/nil", p.Status, p.NextCheckAt)
		}
	})

	t.Run("forbidden target needs attention immediately", func(t *testing.T) {
		tr := pol.Apply(baseProduct(), CheckInput{Outcome: OutcomeForbiddenTarget, Now: t0}, noJitter)
		if tr.Product.Status != StatusNeedsAttention {
			t.Fatalf("status %s", tr.Product.Status)
		}
	})

	t.Run("currency mismatch is an extraction failure", func(t *testing.T) {
		in := CheckInput{Outcome: OutcomeOK, Now: t0,
			Observation: &Observation{Price: Money{5000, "EUR"}, Availability: InStock}}
		tr := pol.Apply(baseProduct(), in, noJitter)
		if tr.Outcome != OutcomeCurrencyMismatch || tr.Alert || tr.CheckStatus != CheckFailed {
			t.Fatalf("got outcome=%s alert=%v status=%s", tr.Outcome, tr.Alert, tr.CheckStatus)
		}
	})

	t.Run("circuit open skips without counting a failure", func(t *testing.T) {
		tr := pol.Apply(baseProduct(), CheckInput{Outcome: OutcomeCircuitOpen, Now: t0, RetryAfter: time.Hour}, noJitter)
		if tr.CheckStatus != CheckSkipped || tr.Product.ConsecutiveFailures != 0 || tr.Product.LastCheckedAt != nil {
			t.Fatalf("unexpected transition %+v", tr)
		}
		if tr.Product.NextCheckAt.Sub(t0) < time.Hour {
			t.Fatal("must not reschedule before the circuit closes")
		}
	})
}

func TestBackoffCaps(t *testing.T) {
	pol := DefaultPolicy()
	if got := pol.Backoff(50); got != pol.MaxBackoff {
		t.Fatalf("Backoff(50) = %v, want %v", got, pol.MaxBackoff)
	}
}

func TestRandomJitterBounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if j := RandomJitter(time.Minute); j < -time.Minute || j > time.Minute {
			t.Fatalf("jitter %v out of bounds", j)
		}
	}
}
