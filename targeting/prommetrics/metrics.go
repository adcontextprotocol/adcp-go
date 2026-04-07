// Package prommetrics provides a Prometheus-compatible implementation of
// targeting.Metrics using only the Go standard library.
//
// Emits Prometheus text exposition format (v0.0.4) at the Handler endpoint.
// No protobuf, no /proc, no external dependencies.
package prommetrics

import (
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
)

// Metrics implements targeting.Metrics backed by a Registry.
type Metrics struct {
	Registry *Registry
}

// Ensure Metrics satisfies the targeting.Metrics interface.
var _ targeting.Metrics = (*Metrics)(nil)

// New creates a Metrics instance with pre-registered targeting metrics.
func New() *Metrics {
	reg := NewRegistry()
	reg.DefineCounter("targeting_context_evaluated_total", "Context evaluation outcomes by stage.", []string{"stage", "passed"})
	reg.DefineCounter("targeting_identity_evaluated_total", "Identity evaluation outcomes by stage.", []string{"stage", "passed"})
	reg.DefineCounter("targeting_exposure_recorded_total", "Total exposure recordings.", nil)
	reg.DefineCounter("targeting_store_errors_total", "Store operation errors by operation.", []string{"operation"})
	reg.DefineHistogram("targeting_stage_duration_seconds", "Time spent in each evaluation stage.", []string{"stage"},
		[]float64{.00001, .00005, .0001, .0005, .001, .005, .01, .05})

	return &Metrics{Registry: reg}
}

func (m *Metrics) ContextEvaluated(_ string, stage string, passed bool) {
	m.Registry.CounterInc("targeting_context_evaluated_total", stage, boolStr(passed))
}

func (m *Metrics) IdentityEvaluated(_ string, stage string, passed bool) {
	m.Registry.CounterInc("targeting_identity_evaluated_total", stage, boolStr(passed))
}

func (m *Metrics) ExposureRecorded(_ string) {
	m.Registry.CounterInc("targeting_exposure_recorded_total")
}

func (m *Metrics) StoreError(operation string, _ error) {
	m.Registry.CounterInc("targeting_store_errors_total", operation)
}

func (m *Metrics) Latency(stage string, d time.Duration) {
	m.Registry.HistogramObserve("targeting_stage_duration_seconds", d.Seconds(), stage)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
