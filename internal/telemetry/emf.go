package telemetry

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// EMF writes CloudWatch Embedded Metric Format lines. In Lambda, stdout
// lines with an "_aws" key are turned into metrics by CloudWatch Logs:
// no PutMetricData calls, no extra latency, no extra IAM.
//
// Cost note: CloudWatch bills per unique metric name + dimension
// combination, so dimensions must stay low-cardinality (environment,
// result class, retailer profile). Never use product or user IDs.
type EMF struct {
	mu        sync.Mutex
	w         io.Writer
	namespace string
	env       string
	now       func() time.Time
}

// NewEMF returns an emitter for namespace, tagging every metric with Env.
func NewEMF(w io.Writer, namespace, env string) *EMF {
	return &EMF{w: w, namespace: namespace, env: env, now: time.Now}
}

// Unit is a CloudWatch metric unit.
type Unit string

const (
	Count        Unit = "Count"
	Milliseconds Unit = "Milliseconds"
	Seconds      Unit = "Seconds"
)

// Metric is one named value (or up to 100 values) in a set.
type Metric struct {
	Name   string
	Unit   Unit
	Values []float64
}

// Set is metrics sharing one set of dimensions.
type Set struct {
	Dimensions map[string]string // in addition to Env
	Metrics    []Metric
}

// Emit writes one EMF line containing every set.
func (e *EMF) Emit(sets ...Set) {
	if e == nil || len(sets) == 0 {
		return
	}
	root := map[string]any{"Env": e.env}
	type metricDef struct {
		Name string `json:"Name"`
		Unit Unit   `json:"Unit"`
	}
	type directive struct {
		Namespace  string      `json:"Namespace"`
		Dimensions [][]string  `json:"Dimensions"`
		Metrics    []metricDef `json:"Metrics"`
	}
	var directives []directive
	for _, s := range sets {
		dims := []string{"Env"}
		for k, v := range s.Dimensions {
			dims = append(dims, k)
			root[k] = v
		}
		d := directive{Namespace: e.namespace, Dimensions: [][]string{dims}}
		for _, m := range s.Metrics {
			if len(m.Values) == 0 {
				continue
			}
			d.Metrics = append(d.Metrics, metricDef{Name: m.Name, Unit: m.Unit})
			if len(m.Values) == 1 {
				root[m.Name] = m.Values[0]
			} else {
				root[m.Name] = m.Values[:min(len(m.Values), 100)] // EMF limit per line
			}
		}
		if len(d.Metrics) > 0 {
			directives = append(directives, d)
		}
	}
	if len(directives) == 0 {
		return
	}
	root["_aws"] = map[string]any{"Timestamp": e.now().UnixMilli(), "CloudWatchMetrics": directives}
	b, err := json.Marshal(root)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.w.Write(append(b, '\n'))
}
