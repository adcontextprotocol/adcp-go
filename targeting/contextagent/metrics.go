package contextagent

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

// Outcome labels paired with stages. Bounded to a fixed set so the
// resulting `<ns>_stage_outcome_total{stage, outcome}` series is
// finite. Mirror identityagent's vocabulary so dashboards and alerts
// can share PromQL across the two agents.
const (
	OutcomePass    = "pass"
	OutcomeFail    = "fail"
	OutcomeTimeout = "timeout"
	OutcomeError   = "error"
)

// Request-level status labels used by RequestCompleted. Bounded to a
// small set: success (200), client_error (4xx), server_error (5xx),
// timeout (504). The handler maps HTTP status to one of these before
// recording.
const (
	StatusOK          = "ok"
	StatusClientError = "client_error"
	StatusServerError = "server_error"
	StatusTimeout     = "timeout"
)

// Recorder is the metric API the context-agent hot path uses. The OTEL
// implementation is the production recorder; tests use noopRecorder.
type Recorder interface {
	RequestStarted(ctx context.Context)
	RequestCompleted(ctx context.Context, status string, d time.Duration)
	StageOutcome(ctx context.Context, stage, outcome string)
	StageDuration(ctx context.Context, stage string, d time.Duration)
	StoreError(ctx context.Context, store string)
	KeystoreRefresh(ctx context.Context, outcome string)
	ShutdownPanic(ctx context.Context)
	HandlerPanic(ctx context.Context)
	BackgroundPanic(ctx context.Context, where string)
}

// MetricsProvider wires together a Prometheus registry, an OTEL meter
// provider that writes to it, and a Recorder. Build is the only
// constructor.
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
// provider. Safe to call when the provider is disabled (no meter
// provider was built).
func (m *MetricsProvider) Shutdown(ctx context.Context) error {
	if m == nil || m.meterProvider == nil {
		return nil
	}
	return errors.Join(
		m.meterProvider.ForceFlush(ctx),
		m.meterProvider.Shutdown(ctx),
	)
}

// BuildMetrics constructs a MetricsProvider per the supplied config.
//
//   - cfg.Enabled=false → provider with a noop recorder and nil
//     Registry; never fails.
//   - cfg.Enabled=true  → constructs prometheus.NewRegistry, plugs it
//     into the OTEL Prometheus exporter, builds the OtelRecorder. Any
//     failure surfaces as a startup error — invalid metrics config
//     fails startup, matching the identity-agent contract.
func BuildMetrics(cfg MetricsConfig) (*MetricsProvider, error) {
	// Install the W3C TraceContext + Baggage propagator regardless of
	// whether metrics are enabled. Future request-path instrumentation
	// (otelhttp) uses it to extract inbound traceparent / baggage
	// headers into r.Context() so downstream code sees the parent span.
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
		resource.WithAttributes(semconv.ServiceNameKey.String("context-agent")))
	if err != nil {
		return nil, fmt.Errorf("init resource: %w", err)
	}
	provider := otelmetric.NewMeterProvider(
		otelmetric.WithReader(exporter),
		otelmetric.WithResource(res),
	)
	rec, err := newOtelRecorder(provider.Meter("context-agent"), cfg.Namespace)
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

// RegisterOpenConnectionsObserver mirrors identityagent: registers an
// OTEL ObservableGauge "<namespace>_open_connections" whose value is
// read from observerFn on every scrape. No-op when metrics are
// disabled. observerFn must be cheap and concurrent-safe.
func (m *MetricsProvider) RegisterOpenConnectionsObserver(observerFn func() int64) error {
	if m == nil || m.meterProvider == nil || observerFn == nil {
		return nil
	}
	meter := m.meterProvider.Meter("context-agent")
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

// RegisterSuppressionSnapshotObservers wires the suppression snapshot's
// health onto three gauges read on every Prometheus scrape:
//
//   - <ns>_suppression_consecutive_failures — current uninterrupted
//     refresh-failure streak; resets to 0 on every success. Alerting
//     on `> 0 for 15m` catches a Valkey outage that the snapshot is
//     surviving on stale data.
//   - <ns>_suppression_last_refresh_unix — Unix-second timestamp of
//     the most recent successful refresh. Pair with `time()` to
//     compute snapshot age in PromQL.
//   - <ns>_suppression_entries{kind=property|geo} — current snapshot
//     size, segmented by suppression dimension.
//
// failureGauge, lastRefreshGauge, and sizeGauge accept callback
// functions so the snapshot's internal atomics drive the metrics
// directly. No-op when metrics are disabled.
func (m *MetricsProvider) RegisterSuppressionSnapshotObservers(
	failures func() int64,
	lastRefreshUnix func() int64,
	propertySize func() int64,
	geoSize func() int64,
) error {
	if m == nil || m.meterProvider == nil {
		return nil
	}
	meter := m.meterProvider.Meter("context-agent")

	if failures != nil {
		g, err := meter.Int64ObservableGauge(fmt.Sprintf("%s_suppression_consecutive_failures", m.namespace))
		if err != nil {
			return fmt.Errorf("register suppression_consecutive_failures: %w", err)
		}
		if _, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(g, failures())
			return nil
		}, g); err != nil {
			return fmt.Errorf("register suppression_consecutive_failures callback: %w", err)
		}
	}
	if lastRefreshUnix != nil {
		g, err := meter.Int64ObservableGauge(fmt.Sprintf("%s_suppression_last_refresh_unix", m.namespace))
		if err != nil {
			return fmt.Errorf("register suppression_last_refresh_unix: %w", err)
		}
		if _, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(g, lastRefreshUnix())
			return nil
		}, g); err != nil {
			return fmt.Errorf("register suppression_last_refresh_unix callback: %w", err)
		}
	}
	if propertySize != nil || geoSize != nil {
		g, err := meter.Int64ObservableGauge(fmt.Sprintf("%s_suppression_entries", m.namespace))
		if err != nil {
			return fmt.Errorf("register suppression_entries: %w", err)
		}
		if _, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			if propertySize != nil {
				o.ObserveInt64(g, propertySize(), metric.WithAttributes(stringAttr("kind", "property")))
			}
			if geoSize != nil {
				o.ObserveInt64(g, geoSize(), metric.WithAttributes(stringAttr("kind", "geo")))
			}
			return nil
		}, g); err != nil {
			return fmt.Errorf("register suppression_entries callback: %w", err)
		}
	}
	return nil
}

// otelRecorder is the production Recorder.
type otelRecorder struct {
	requestStarted   metric.Int64Counter
	requestDuration  metric.Float64Histogram
	stageOutcome     metric.Int64Counter
	stageDuration    metric.Float64Histogram
	storeError       metric.Int64Counter
	keystoreRefresh  metric.Int64Counter
	shutdownPanic    metric.Int64Counter
	handlerPanic     metric.Int64Counter
	backgroundPanic  metric.Int64Counter
}

var _ Recorder = (*otelRecorder)(nil)

func newOtelRecorder(meter metric.Meter, namespace string) (*otelRecorder, error) {
	requestStarted, err := meter.Int64Counter(fmt.Sprintf("%s_requests_started_total", namespace))
	if err != nil {
		return nil, err
	}
	requestDuration, err := meter.Float64Histogram(fmt.Sprintf("%s_request_duration_seconds", namespace))
	if err != nil {
		return nil, err
	}
	stageOutcome, err := meter.Int64Counter(fmt.Sprintf("%s_stage_outcome_total", namespace))
	if err != nil {
		return nil, err
	}
	stageDuration, err := meter.Float64Histogram(fmt.Sprintf("%s_stage_duration_seconds", namespace))
	if err != nil {
		return nil, err
	}
	storeError, err := meter.Int64Counter(fmt.Sprintf("%s_store_errors_total", namespace))
	if err != nil {
		return nil, err
	}
	keystoreRefresh, err := meter.Int64Counter(fmt.Sprintf("%s_keystore_refresh_total", namespace))
	if err != nil {
		return nil, err
	}
	shutdownPanic, err := meter.Int64Counter(fmt.Sprintf("%s_shutdown_panic_total", namespace))
	if err != nil {
		return nil, err
	}
	handlerPanic, err := meter.Int64Counter(fmt.Sprintf("%s_handler_panic_total", namespace))
	if err != nil {
		return nil, err
	}
	backgroundPanic, err := meter.Int64Counter(fmt.Sprintf("%s_background_panic_total", namespace))
	if err != nil {
		return nil, err
	}
	return &otelRecorder{
		requestStarted:  requestStarted,
		requestDuration: requestDuration,
		stageOutcome:    stageOutcome,
		stageDuration:   stageDuration,
		storeError:      storeError,
		keystoreRefresh: keystoreRefresh,
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

func (r *otelRecorder) KeystoreRefresh(ctx context.Context, outcome string) {
	r.keystoreRefresh.Add(ctx, 1, metric.WithAttributes(stringAttr("outcome", outcome)))
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

// noopRecorder is used when metrics are disabled or in tests.
type noopRecorder struct{}

func (noopRecorder) RequestStarted(context.Context)                          {}
func (noopRecorder) RequestCompleted(context.Context, string, time.Duration) {}
func (noopRecorder) StageOutcome(context.Context, string, string)            {}
func (noopRecorder) StageDuration(context.Context, string, time.Duration)    {}
func (noopRecorder) StoreError(context.Context, string)                      {}
func (noopRecorder) KeystoreRefresh(context.Context, string)                 {}
func (noopRecorder) ShutdownPanic(context.Context)                           {}
func (noopRecorder) HandlerPanic(context.Context)                            {}
func (noopRecorder) BackgroundPanic(context.Context, string)                 {}

// targetingMetricsAdapter projects the agent's Recorder onto the
// targeting.Metrics interface the ContextEngine wants. The engine emits
// stage-name strings; the adapter passes them through. Latency calls
// become stage_duration_seconds histogram observations.
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
