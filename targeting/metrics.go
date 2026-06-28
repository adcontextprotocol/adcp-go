package targeting

import (
	"context"
	"time"
)

// Evaluation stage constants.
//
// StageTopicNoTaxonomy is emitted instead of StageTopicMatch when a
// TopicTargets package is short-circuited because the engine has no
// accepted taxonomies configured. The separate label lets on-call
// distinguish a configuration drop (no taxonomies declared) from a
// genuine miss (taxonomy declared, no matching topic) in dashboards.
const (
	StagePropertyBitmap  = "property_bitmap"
	StageSuppression     = "suppression"
	StageSignature       = "signature"
	StageDirectMatch     = "direct_match"
	StageURLFilter       = "url_filter"
	StageTopicMatch      = "topic_match"
	StageTopicNoTaxonomy = "topic_no_taxonomy"
	StageAudience        = "audience"
	StageSignalMatch     = "signal_match"
)

// Metrics receives instrumentation callbacks from the targeting engine.
// Implementations can map these to Prometheus counters or structured logging.
// The noop default adds zero overhead.
//
// Each method receives the request-scoped context so implementations can
// attach trace/baggage data to the emitted record. Implementations MUST NOT
// retain the context past the call.
//
// Labels are intentionally low-cardinality. Per-package metrics would
// explode cardinality at the scale of hundreds of thousands of packages
// for negligible operational value; callers that need per-package data
// should sample request logs instead.
type Metrics interface {
	ContextEvaluated(ctx context.Context, stage string, passed bool)
	IdentityEvaluated(ctx context.Context, stage string, passed bool)
	StoreError(ctx context.Context, operation string, err error)
	Latency(ctx context.Context, stage string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) ContextEvaluated(context.Context, string, bool)  {}
func (noopMetrics) IdentityEvaluated(context.Context, string, bool) {}
func (noopMetrics) StoreError(context.Context, string, error)       {}
func (noopMetrics) Latency(context.Context, string, time.Duration)  {}
