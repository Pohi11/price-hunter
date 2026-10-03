package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/checker"
	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/scheduler"
	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// lines parses EMF output and checks the format's invariants: the
// namespace, and that every declared dimension and metric has a root value.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var root map[string]any
		if err := json.Unmarshal([]byte(l), &root); err != nil {
			t.Fatalf("invalid EMF JSON %q: %v", l, err)
		}
		var typed struct {
			AWS struct {
				Timestamp         int64
				CloudWatchMetrics []struct {
					Namespace  string
					Dimensions [][]string
					Metrics    []struct{ Name, Unit string }
				}
			} `json:"_aws"`
		}
		_ = json.Unmarshal([]byte(l), &typed)
		if typed.AWS.Timestamp == 0 || len(typed.AWS.CloudWatchMetrics) == 0 {
			t.Fatalf("missing _aws metadata: %s", l)
		}
		for _, d := range typed.AWS.CloudWatchMetrics {
			if d.Namespace != Namespace {
				t.Fatalf("namespace %q", d.Namespace)
			}
			for _, dims := range d.Dimensions {
				for _, k := range dims {
					if _, ok := root[k]; !ok {
						t.Fatalf("dimension %s has no value in %s", k, l)
					}
				}
			}
			for _, met := range d.Metrics {
				if _, ok := root[met.Name]; !ok {
					t.Fatalf("metric %s has no value in %s", met.Name, l)
				}
			}
		}
		out = append(out, root)
	}
	return out
}

func TestCheckMetricsShapeAndCardinality(t *testing.T) {
	var buf bytes.Buffer
	r := New(telemetry.NewEMF(&buf, Namespace, "dev"), []string{"demostore", "books-toscrape"})
	r.CheckCompleted(context.Background(), checker.Report{Outcome: domain.OutcomeOK, Status: domain.CheckSucceeded,
		Retailer: "demostore", Alert: true, FetchDuration: 120 * time.Millisecond, TotalDuration: 300 * time.Millisecond})
	r.CheckCompleted(context.Background(), checker.Report{Outcome: domain.OutcomePriceNotFound, Status: domain.CheckFailed,
		Retailer: "some-unprofiled-shop"})
	ls := lines(t, &buf)
	if len(ls) != 2 {
		t.Fatalf("%d lines", len(ls))
	}
	if ls[0]["Result"] != "success" || ls[0]["AlertsTriggered"] != 1.0 || ls[0]["FetchLatencyMs"] != 120.0 || ls[0]["Env"] != "dev" {
		t.Fatalf("success line: %v", ls[0])
	}
	if ls[1]["Result"] != "extraction_failure" || ls[1]["Retailer"] != "other" || ls[1]["ExtractionFailures"] != 1.0 {
		t.Fatalf("failure line: %v", ls[1])
	}
	for _, l := range ls {
		for k := range l {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "product") || strings.Contains(lk, "user") || strings.Contains(lk, "check_id") {
				t.Fatalf("high-cardinality key %q in metrics", k)
			}
		}
	}
}

func TestResultClasses(t *testing.T) {
	cases := map[string]checker.Report{
		ResultDuplicate:         {Duplicate: true},
		ResultSkipped:           {Status: domain.CheckSkipped, Outcome: domain.OutcomeCircuitOpen},
		ResultSuccess:           {Outcome: domain.OutcomeOK, Status: domain.CheckSucceeded},
		ResultExtractionFailure: {Outcome: domain.OutcomeAmbiguousPrice, Status: domain.CheckFailed},
		ResultRetailerError:     {Outcome: domain.OutcomeBlocked, Status: domain.CheckFailed},
	}
	for want, rep := range cases {
		if got := ResultOf(rep); got != want {
			t.Errorf("ResultOf(%+v) = %s, want %s", rep, got, want)
		}
	}
}

func TestSchedulerLagSamplesChunkedAt100(t *testing.T) {
	var buf bytes.Buffer
	r := New(telemetry.NewEMF(&buf, Namespace, "prod"), nil)
	st := scheduler.Stats{Enqueued: 250}
	for i := 0; i < 250; i++ {
		st.Lags = append(st.Lags, time.Duration(i)*time.Second)
	}
	r.SchedulerRun(context.Background(), st)
	ls := lines(t, &buf)
	if len(ls) != 3 {
		t.Fatalf("%d lines, want 3 (100+100+50 samples)", len(ls))
	}
	if n := len(ls[2]["ScheduleLagSeconds"].([]any)); n != 50 {
		t.Fatalf("last chunk %d", n)
	}
	if ls[0]["ProductsEnqueued"] != 250.0 || ls[1]["ProductsEnqueued"] != nil {
		t.Fatal("run totals must be emitted exactly once")
	}

	// A run with nothing due still reports zeros, keeping graphs continuous.
	buf.Reset()
	r.SchedulerRun(context.Background(), scheduler.Stats{})
	if l := lines(t, &buf); len(l) != 1 || l[0]["ProductsEnqueued"] != 0.0 {
		t.Fatalf("empty run: %v", l)
	}
}

func TestNotificationMetric(t *testing.T) {
	var buf bytes.Buffer
	New(telemetry.NewEMF(&buf, Namespace, "dev"), nil).NotificationDone(context.Background(), domain.NotificationFailed)
	ls := lines(t, &buf)
	if ls[0]["Status"] != "FAILED" || ls[0]["Notifications"] != 1.0 {
		t.Fatalf("%v", ls[0])
	}
}
