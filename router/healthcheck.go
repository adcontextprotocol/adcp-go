package router

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// HealthCheckMetrics is the metrics interface used by the health checker.
// Implemented by the cmd layer to bridge to prommetrics without a direct dependency.
type HealthCheckMetrics interface {
	SetHealthStatus(providerID string, healthy bool)
	ObserveCheckDuration(providerID string, ms float64)
	IncExcluded(providerID string)
	IncRecovered(providerID string)
}

// HealthChecker polls provider health endpoints in the background.
type HealthChecker struct {
	providers *ProviderSet
	health    *ProviderHealth
	client    *http.Client
	logger    *slog.Logger
	interval  time.Duration
	timeout   time.Duration
	metrics   HealthCheckMetrics
	stop      chan struct{}
	done      chan struct{}
}

// HealthCheckerOption configures a HealthChecker.
type HealthCheckerOption func(*HealthChecker)

// WithHealthCheckMetrics sets the metrics callback for the health checker.
func WithHealthCheckMetrics(m HealthCheckMetrics) HealthCheckerOption {
	return func(hc *HealthChecker) { hc.metrics = m }
}

// WithHealthCheckLogger sets the logger for the health checker.
func WithHealthCheckLogger(l *slog.Logger) HealthCheckerOption {
	return func(hc *HealthChecker) { hc.logger = l }
}

// WithHealthCheckClient sets a custom HTTP client. For tests only —
// production should use the default client with safeDialContext.
func WithHealthCheckClient(c *http.Client) HealthCheckerOption {
	return func(hc *HealthChecker) { hc.client = c }
}

// NewHealthChecker creates a health checker that polls provider /health endpoints.
func NewHealthChecker(providers *ProviderSet, health *ProviderHealth, cfg HealthCheckConfig, opts ...HealthCheckerOption) *HealthChecker {
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	hc := &HealthChecker{
		providers: providers,
		health:    health,
		client: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{DialContext: safeDialContext},
		},
		logger:    slog.Default(),
		interval:  interval,
		timeout:   timeout,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	for _, o := range opts {
		o(hc)
	}
	return hc
}

// Preflight checks all providers once. Logs warnings for unreachable providers
// but does not block startup. Does not record failures against the circuit breaker.
func (hc *HealthChecker) Preflight(ctx context.Context) {
	for _, p := range hc.providers.All() {
		if p.EffectiveStatus() == ProviderStatusInactive {
			continue
		}
		if err := hc.checkProvider(ctx, p); err != nil {
			hc.logger.Warn("provider health check failed during preflight",
				"provider", p.ID, "endpoint", p.Endpoint, "error", err)
		}
	}
}

// Start begins background health polling. Call Stop to shut down.
func (hc *HealthChecker) Start() {
	go hc.run()
}

// Stop gracefully shuts down the health checker.
func (hc *HealthChecker) Stop() {
	close(hc.stop)
	<-hc.done
}

func (hc *HealthChecker) run() {
	defer close(hc.done)
	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-hc.stop:
			return
		case <-ticker.C:
			hc.checkAll()
		}
	}
}

func (hc *HealthChecker) checkAll() {
	for _, p := range hc.providers.All() {
		if p.EffectiveStatus() == ProviderStatusInactive {
			continue
		}

		wasOpen := hc.health.IsCircuitOpen(p.ID)

		ctx, cancel := context.WithTimeout(context.Background(), hc.timeout)
		start := time.Now()
		err := hc.checkProvider(ctx, p)
		duration := time.Since(start)
		cancel()

		if hc.metrics != nil {
			hc.metrics.ObserveCheckDuration(p.ID, float64(duration.Milliseconds()))
		}

		if err != nil {
			hc.logger.Debug("provider health check failed", "provider", p.ID, "error", err)
			hc.health.RecordFailure(p.ID)
			if hc.metrics != nil {
				hc.metrics.SetHealthStatus(p.ID, false)
			}
		} else {
			hc.health.RecordSuccess(p.ID)
			if hc.metrics != nil {
				hc.metrics.SetHealthStatus(p.ID, true)
			}
			if wasOpen {
				hc.logger.Info("provider recovered", "provider", p.ID)
				if hc.metrics != nil {
					hc.metrics.IncRecovered(p.ID)
				}
			}
		}
	}
}

func (hc *HealthChecker) checkProvider(ctx context.Context, p ProviderConfig) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.Endpoint+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := hc.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return &healthCheckError{statusCode: resp.StatusCode}
	}
	return nil
}

type healthCheckError struct {
	statusCode int
}

func (e *healthCheckError) Error() string {
	return "health check returned " + http.StatusText(e.statusCode)
}
