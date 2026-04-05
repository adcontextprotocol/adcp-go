package targeting

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
// Implementations can map these to Prometheus counters, OpenTelemetry spans,
// or structured logging. The noop default adds zero overhead.
type Metrics interface {
	ContextEvaluated(packageID, stage string, passed bool)
	IdentityEvaluated(packageID, stage string, passed bool)
	ExposureRecorded(packageID string)
	StoreError(operation string, err error)
}

type noopMetrics struct{}

func (noopMetrics) ContextEvaluated(string, string, bool) {}
func (noopMetrics) IdentityEvaluated(string, string, bool) {}
func (noopMetrics) ExposureRecorded(string) {}
func (noopMetrics) StoreError(string, error) {}
