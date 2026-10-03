// Package metrics turns service reports into CloudWatch metrics (EMF).
//
// The metric set is deliberately small and low-cardinality: CloudWatch
// bills per unique metric name + dimension combination (~$0.30/month each
// beyond the free 10). Detail (exact outcome codes, product IDs) lives in
// the structured logs, queryable with Logs Insights.
package metrics

import (
	"context"

	"github.com/Pohi11/price-hunter/internal/checker"
	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/scheduler"
	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// Namespace is the CloudWatch namespace for all custom metrics.
const Namespace = "PriceHunter"

// Recorder implements checker.Observer, scheduler.Observer and notify.Observer.
type Recorder struct {
	emf       *telemetry.EMF
	retailers map[string]bool
}

// New returns a Recorder. Retailer dimensions are limited to known profile
// IDs; anything else is reported as "other".
func New(emf *telemetry.EMF, retailerIDs []string) *Recorder {
	known := map[string]bool{"generic": true}
	for _, id := range retailerIDs {
		known[id] = true
	}
	return &Recorder{emf: emf, retailers: known}
}

// Result classes for ChecksCompleted: five values, so five metrics.
const (
	ResultSuccess           = "success"
	ResultExtractionFailure = "extraction_failure" // page loaded, no usable price: parser drift
	ResultRetailerError     = "retailer_error"     // retailer unreachable, blocking, removed, ...
	ResultSkipped           = "skipped"            // circuit open, product inactive
	ResultDuplicate         = "duplicate"          // redelivered message, already handled
)

// ResultOf classifies a check report.
func ResultOf(r checker.Report) string {
	switch {
	case r.Duplicate:
		return ResultDuplicate
	case r.Status == domain.CheckSkipped:
		return ResultSkipped
	case r.Outcome == domain.OutcomeOK:
		return ResultSuccess
	case r.Outcome.IsExtractionFailure():
		return ResultExtractionFailure
	default:
		return ResultRetailerError
	}
}

func (m *Recorder) retailer(id string) string {
	if m.retailers[id] {
		return id
	}
	return "other"
}

func count(name string) telemetry.Metric {
	return telemetry.Metric{Name: name, Unit: telemetry.Count, Values: []float64{1}}
}

// CheckCompleted records one finished check.
func (m *Recorder) CheckCompleted(_ context.Context, r checker.Report) {
	result := ResultOf(r)
	sets := []telemetry.Set{{Dimensions: map[string]string{"Result": result}, Metrics: []telemetry.Metric{count("ChecksCompleted")}}}

	env := []telemetry.Metric{{Name: "CheckDurationMs", Unit: telemetry.Milliseconds, Values: []float64{float64(r.TotalDuration.Milliseconds())}}}
	if r.FetchDuration > 0 {
		env = append(env, telemetry.Metric{Name: "FetchLatencyMs", Unit: telemetry.Milliseconds, Values: []float64{float64(r.FetchDuration.Milliseconds())}})
	}
	if r.Alert {
		env = append(env, count("AlertsTriggered"))
	}
	if r.Suspect {
		env = append(env, count("SuspectPrices"))
	}
	if r.CircuitOpened {
		env = append(env, count("CircuitOpened"))
	}
	if r.Disagreement {
		env = append(env, count("StrategyDisagreements"))
	}
	sets = append(sets, telemetry.Set{Metrics: env})

	if result == ResultExtractionFailure {
		sets = append(sets, telemetry.Set{
			Dimensions: map[string]string{"Retailer": m.retailer(r.Retailer)},
			Metrics:    []telemetry.Metric{count("ExtractionFailures")},
		})
	}
	m.emf.Emit(sets...)
}

// SchedulerRun records one scheduling pass, including every leased
// product's lag so the SLO percentile is computed over products, not runs.
func (m *Recorder) SchedulerRun(_ context.Context, s scheduler.Stats) {
	totals := []telemetry.Metric{
		{Name: "ProductsEnqueued", Unit: telemetry.Count, Values: []float64{float64(s.Enqueued)}},
		{Name: "EnqueueFailures", Unit: telemetry.Count, Values: []float64{float64(s.EnqueueFailed)}},
	}
	if s.SkippedBackpressure {
		totals = append(totals, count("SchedulerBackpressureSkips"))
	}
	lags := make([]float64, len(s.Lags))
	for i, l := range s.Lags {
		lags[i] = l.Seconds()
	}
	for first := true; first || len(lags) > 0; first = false {
		n := min(len(lags), 100) // EMF allows 100 values per metric per line
		ms := []telemetry.Metric{{Name: "ScheduleLagSeconds", Unit: telemetry.Seconds, Values: lags[:n]}}
		if first {
			ms = append(ms, totals...)
		}
		m.emf.Emit(telemetry.Set{Metrics: ms})
		lags = lags[n:]
	}
}

// NotificationDone records a delivery result (SENT, FAILED, SKIPPED).
func (m *Recorder) NotificationDone(_ context.Context, s domain.NotificationStatus) {
	m.emf.Emit(telemetry.Set{Dimensions: map[string]string{"Status": string(s)}, Metrics: []telemetry.Metric{count("Notifications")}})
}
