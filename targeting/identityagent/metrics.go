package identityagent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	otelmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"

	"github.com/adcontextprotocol/adcp-go/targeting"
)

// Stage labels for the request pipeline. Values are bounded for low
// cardinality.
const (
	StageResolve  = "resolve"
	StageFCap     = "fcap"
	StageAudience = "audience"
	StageRequest  = "request"
	StageTMPX     = "tmpx"
)

// Outcome labels paired with stages. Bounded to a fixed set.
const (
	OutcomePass     = "pass"
	OutcomeFail     = "fail"
	OutcomeTimeout  = "timeout"
	OutcomeError    = "error"
	OutcomeCanceled = "canceled"
)

// Recorder is the metric API the identity-agent hot path uses. The OTEL
// implementation is the production recorder; tests use noopRecorder.
type Recorder interface {
	RequestStarted(ctx context.Context)
	RequestCompleted(ctx context.Context, status string, d time.Duration)
	StageOutcome(ctx context.Context, stage, outcome string)
	StageDuration(ctx context.Context, stage string, d time.Duration)
	StoreError(ctx context.Context, store string)
	ConfigRefresh(ctx context.Context, outcome string)
	ShutdownPanic(ctx context.Context)
	HandlerPanic(ctx context.Context)
	BackgroundPanic(ctx context.Context, where string)
}

// MetricsProvider wires together a Prometheus registry, an OTEL meter
// provider that writes to it, and a Recorder. Build is the only constructor.
//
// A disabled provider returns a noop Recorder and a nil Registry. The
// /metrics endpoint isn't mounted when Registry is nil.
type MetricsProvider struct {
	Registry *prometheus.Registry
	Recorder Recorder

	meterProvider *otelmetric.MeterProvider
	namespace     string
}

// Shutdown flushes any pending metric data and tears down the meter
// provider. Safe to call when the provider is disabled (no meter provider
// was built).
func (m *MetricsProvider) Shutdown(ctx context.Context) error {
	if m == nil || m.meterProvider == nil {
		return nil
	}
	return errors.Join(
		m.meterProvider.ForceFlush(ctx),
		m.meterProvider.Shutdown(ctx),
	)
}

// Build constructs a MetricsProvider per the supplied config.
//
//   - cfg.Enabled=false → returns a provider with a noop recorder and nil
//     Registry, never fails.
//   - cfg.Enabled=true  → constructs prometheus.NewRegistry, plugs it into
//     the OTEL Prometheus exporter, builds the OtelRecorder. Any failure
//     surfaces as a startup error per the contract: invalid metrics config
//     fails startup.
func Build(cfg MetricsConfig) (*MetricsProvider, error) {
	// Install the W3C TraceContext + Baggage propagator regardless of
	// whether metrics are enabled. otelhttp on the request path uses it to
	// extract inbound traceparent / baggage headers into r.Context() so
	// downstream code (and any future TracerProvider) sees the parent span.
	// No spans are created in-process until a TracerProvider is configured;
	// extraction is the only effect.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !cfg.Enabled {
		return &MetricsProvider{Recorder: noopRecorder{}}, nil
	}
	reg := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("init prometheus exporter: %w", err)
	}
	res, err := resource.New(context.Background(),
		resource.WithAttributes(semconv.ServiceNameKey.String("identity-agent")))
	if err != nil {
		return nil, fmt.Errorf("init resource: %w", err)
	}
	provider := otelmetric.NewMeterProvider(
		otelmetric.WithReader(exporter),
		otelmetric.WithResource(res),
	)
	rec, err := newOtelRecorder(provider.Meter("identity-agent"), cfg.Namespace)
	if err != nil {
		return nil, fmt.Errorf("init otel recorder: %w", err)
	}
	return &MetricsProvider{
		Registry:      reg,
		Recorder:      rec,
		meterProvider: provider,
		namespace:     cfg.Namespace,
	}, nil
}

// RegisterOpenConnectionsObserver registers an OTEL ObservableGauge named
// "<namespace>_open_connections" whose value is read from observerFn each
// time the Prometheus exporter scrapes. No-op when metrics are disabled —
// the gauge simply isn't published.
//
// observerFn is invoked from arbitrary goroutines on the exporter's
// schedule; it must be safe to call concurrently and should be cheap
// (an atomic load is the intended shape).
func (m *MetricsProvider) RegisterOpenConnectionsObserver(observerFn func() int64) error {
	if m == nil || m.meterProvider == nil || observerFn == nil {
		return nil
	}
	meter := m.meterProvider.Meter("identity-agent")
	gauge, err := meter.Int64ObservableGauge(fmt.Sprintf("%s_open_connections", m.namespace))
	if err != nil {
		return fmt.Errorf("register open_connections gauge: %w", err)
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(gauge, observerFn())
		return nil
	}, gauge)
	if err != nil {
		return fmt.Errorf("register open_connections callback: %w", err)
	}
	return nil
}

// otelRecorder is the production Recorder. It uses an OTEL meter with the
// Prometheus exporter to surface metrics on the Prometheus registry the
// /metrics handler reads from.
type otelRecorder struct {
	requestStarted  metric.Int64Counter
	requestDuration metric.Float64Histogram
	stageOutcome    metric.Int64Counter
	stageDuration   metric.Float64Histogram
	storeError      metric.Int64Counter
	configRefresh   metric.Int64Counter
	shutdownPanic   metric.Int64Counter
	handlerPanic    metric.Int64Counter
	backgroundPanic metric.Int64Counter
}

var _ Recorder = (*otelRecorder)(nil)

func newOtelRecorder(meter metric.Meter, namespace string) (*otelRecorder, error) {
	requestStarted, err := meter.Int64Counter(
		fmt.Sprintf("%s_requests_started_total", namespace))
	if err != nil {
		return nil, err
	}
	requestDuration, err := meter.Float64Histogram(
		fmt.Sprintf("%s_request_duration_seconds", namespace))
	if err != nil {
		return nil, err
	}
	stageOutcome, err := meter.Int64Counter(
		fmt.Sprintf("%s_stage_outcome_total", namespace))
	if err != nil {
		return nil, err
	}
	stageDuration, err := meter.Float64Histogram(
		fmt.Sprintf("%s_stage_duration_seconds", namespace))
	if err != nil {
		return nil, err
	}
	storeError, err := meter.Int64Counter(
		fmt.Sprintf("%s_store_errors_total", namespace))
	if err != nil {
		return nil, err
	}
	configRefresh, err := meter.Int64Counter(
		fmt.Sprintf("%s_config_refresh_total", namespace))
	if err != nil {
		return nil, err
	}
	shutdownPanic, err := meter.Int64Counter(
		fmt.Sprintf("%s_shutdown_panic_total", namespace))
	if err != nil {
		return nil, err
	}
	handlerPanic, err := meter.Int64Counter(
		fmt.Sprintf("%s_handler_panic_total", namespace))
	if err != nil {
		return nil, err
	}
	backgroundPanic, err := meter.Int64Counter(
		fmt.Sprintf("%s_background_panic_total", namespace))
	if err != nil {
		return nil, err
	}
	return &otelRecorder{
		requestStarted:  requestStarted,
		requestDuration: requestDuration,
		stageOutcome:    stageOutcome,
		stageDuration:   stageDuration,
		storeError:      storeError,
		configRefresh:   configRefresh,
		shutdownPanic:   shutdownPanic,
		handlerPanic:    handlerPanic,
		backgroundPanic: backgroundPanic,
	}, nil
}

func (r *otelRecorder) RequestStarted(ctx context.Context) {
	r.requestStarted.Add(ctx, 1)
}

func (r *otelRecorder) RequestCompleted(ctx context.Context, status string, d time.Duration) {
	r.requestDuration.Record(ctx, d.Seconds(), metric.WithAttributes(stringAttr("status", status)))
}

func (r *otelRecorder) StageOutcome(ctx context.Context, stage, outcome string) {
	r.stageOutcome.Add(ctx, 1, metric.WithAttributes(
		stringAttr("stage", stage),
		stringAttr("outcome", outcome),
	))
}

func (r *otelRecorder) StageDuration(ctx context.Context, stage string, d time.Duration) {
	r.stageDuration.Record(ctx, d.Seconds(), metric.WithAttributes(stringAttr("stage", stage)))
}

func (r *otelRecorder) StoreError(ctx context.Context, store string) {
	r.storeError.Add(ctx, 1, metric.WithAttributes(stringAttr("store", store)))
}

func (r *otelRecorder) ConfigRefresh(ctx context.Context, outcome string) {
	r.configRefresh.Add(ctx, 1, metric.WithAttributes(stringAttr("outcome", outcome)))
}

func (r *otelRecorder) ShutdownPanic(ctx context.Context) {
	r.shutdownPanic.Add(ctx, 1)
}

func (r *otelRecorder) HandlerPanic(ctx context.Context) {
	r.handlerPanic.Add(ctx, 1)
}

func (r *otelRecorder) BackgroundPanic(ctx context.Context, where string) {
	r.backgroundPanic.Add(ctx, 1, metric.WithAttributes(stringAttr("where", where)))
}

// noopRecorder is used when metrics are disabled or in tests. Every method
// is a no-op.
type noopRecorder struct{}

func (noopRecorder) RequestStarted(context.Context)                          {}
func (noopRecorder) RequestCompleted(context.Context, string, time.Duration) {}
func (noopRecorder) StageOutcome(context.Context, string, string)            {}
func (noopRecorder) StageDuration(context.Context, string, time.Duration)    {}
func (noopRecorder) StoreError(context.Context, string)                      {}
func (noopRecorder) ConfigRefresh(context.Context, string)                   {}
func (noopRecorder) ShutdownPanic(context.Context)                           {}
func (noopRecorder) HandlerPanic(context.Context)                            {}
func (noopRecorder) BackgroundPanic(context.Context, string)                 {}

// targetingMetricsAdapter projects the agent's Recorder onto the
// targeting.Metrics interface the IdentityEngine wants. The engine emits
// stage-name strings; the adapter passes them through. Latency calls become
// stage_duration_seconds histogram observations.
type targetingMetricsAdapter struct {
	recorder Recorder
}

var _ targeting.Metrics = (*targetingMetricsAdapter)(nil)

func newTargetingMetricsAdapter(r Recorder) targeting.Metrics {
	if r == nil {
		return nil
	}
	return &targetingMetricsAdapter{recorder: r}
}

func (a *targetingMetricsAdapter) ContextEvaluated(ctx context.Context, stage string, passed bool) {
	a.recorder.StageOutcome(ctx, stage, outcomeFromPassed(passed))
}

func (a *targetingMetricsAdapter) IdentityEvaluated(ctx context.Context, stage string, passed bool) {
	a.recorder.StageOutcome(ctx, stage, outcomeFromPassed(passed))
}

func (a *targetingMetricsAdapter) StoreError(ctx context.Context, operation string, _ error) {
	a.recorder.StoreError(ctx, operation)
}

func (a *targetingMetricsAdapter) Latency(ctx context.Context, stage string, d time.Duration) {
	a.recorder.StageDuration(ctx, stage, d)
}

func outcomeFromPassed(passed bool) string {
	if passed {
		return OutcomePass
	}
	return OutcomeFail
}

// stringAttr is a thin alias for attribute.String to keep call sites tight.
func stringAttr(key, value string) attribute.KeyValue {
	return attribute.String(key, value)
}
