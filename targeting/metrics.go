package targeting

import "time"

// Evaluation stage constants.
const (
	StagePropertyBitmap = "property_bitmap"
	StageSuppression    = "suppression"
	StageSignature      = "signature"
	StageURLFilter      = "url_filter"
	StageTopicMatch     = "topic_match"
	StageAudience       = "audience"
)

// Metrics receives instrumentation callbacks from the targeting engine.
// Implementations can map these to Prometheus counters or structured logging.
// The noop default adds zero overhead.
//
// Labels are intentionally low-cardinality. Per-package metrics would
// explode cardinality at the scale of hundreds of thousands of packages
// for negligible operational value; callers that need per-package data
// should sample request logs instead.
type Metrics interface {
	ContextEvaluated(stage string, passed bool)
	IdentityEvaluated(stage string, passed bool)
	StoreError(operation string, err error)
	Latency(stage string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) ContextEvaluated(string, bool)  {}
func (noopMetrics) IdentityEvaluated(string, bool) {}
func (noopMetrics) StoreError(string, error)       {}
func (noopMetrics) Latency(string, time.Duration)  {}
