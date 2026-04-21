package targeting

import "time"

// Evaluation stage constants.
const (
	StagePropertyBitmap = "property_bitmap"
	StageSuppression    = "suppression"
	StageSignature      = "signature"
	StageURLFilter      = "url_filter"
	StageTopicMatch     = "topic_match"
	StageCampaignFreq   = "campaign_freq"
	StagePackageFreq    = "package_freq"
	StageAudience       = "audience"
)

// Metrics receives instrumentation callbacks from the targeting engine.
// Implementations can map these to Prometheus counters or structured logging.
// The noop default adds zero overhead.
type Metrics interface {
	ContextEvaluated(packageID, stage string, passed bool)
	IdentityEvaluated(packageID, stage string, passed bool)
	StoreError(operation string, err error)
	Latency(stage string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) ContextEvaluated(string, string, bool) {}
func (noopMetrics) IdentityEvaluated(string, string, bool) {}
func (noopMetrics) StoreError(string, error)              {}
func (noopMetrics) Latency(string, time.Duration)         {}
