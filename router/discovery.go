package router

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// DiscoveryConfig controls dynamic provider discovery.
// When Endpoint is empty, discovery is disabled.
type DiscoveryConfig struct {
	Endpoint        string `json:"endpoint"`
	IntervalSeconds int    `json:"interval_seconds"` // default 30
	TimeoutSeconds  int    `json:"timeout_seconds"`  // default 10
}

// Discovery polls an HTTP endpoint for provider registrations and reconciles
// the router's provider set.
type Discovery struct {
	providers     *ProviderSet
	health        *ProviderHealth
	client        *http.Client
	logger        *slog.Logger
	endpoint      string
	interval      time.Duration
	timeout       time.Duration
	latencyBudget time.Duration
	stop          chan struct{}
	done          chan struct{}
}

// DiscoveryOption configures a Discovery instance.
type DiscoveryOption func(*Discovery)

// WithDiscoveryLogger sets the logger for discovery.
func WithDiscoveryLogger(l *slog.Logger) DiscoveryOption {
	return func(d *Discovery) { d.logger = l }
}

// WithDiscoveryClient sets a custom HTTP client. For tests only —
// production should use the default client with safeDialContext.
func WithDiscoveryClient(c *http.Client) DiscoveryOption {
	return func(d *Discovery) { d.client = c }
}

// NewDiscovery creates a provider discovery poller.
func NewDiscovery(providers *ProviderSet, health *ProviderHealth, cfg DiscoveryConfig, latencyBudget time.Duration, opts ...DiscoveryOption) *Discovery {
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	d := &Discovery{
		providers:     providers,
		health:        health,
		client: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{DialContext: safeDialContext},
		},
		logger:        slog.Default(),
		endpoint:      cfg.Endpoint,
		interval:      interval,
		timeout:       timeout,
		latencyBudget: latencyBudget,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Start begins background discovery polling. Call Stop to shut down.
func (d *Discovery) Start() {
	go d.run()
}

// Stop gracefully shuts down discovery.
func (d *Discovery) Stop() {
	close(d.stop)
	<-d.done
}

func (d *Discovery) run() {
	defer close(d.done)

	// Poll immediately on start, then on interval.
	d.pollAndLog()

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.pollAndLog()
		}
	}
}

func (d *Discovery) pollAndLog() {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	if err := d.poll(ctx); err != nil {
		d.logger.Error("discovery poll failed", "endpoint", d.endpoint, "error", err)
	}
}

func (d *Discovery) poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", d.endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return &healthCheckError{statusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB max
	if err != nil {
		return err
	}

	var wire []tmproto.ProviderRegistration
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}

	const maxDiscoveredProviders = 500

	// Convert wire registrations to router configs, validate, and filter.
	valid := make([]ProviderConfig, 0, len(wire))
	for i := range wire {
		p := ProviderConfigFromRegistration(&wire[i])
		if err := ValidateProviderEndpoint(p.Endpoint); err != nil {
			d.logger.Warn("discovery: skipping provider with invalid endpoint",
				"provider", p.ID, "error", err)
			continue
		}
		if err := ValidateProviderConfig(&p, d.latencyBudget); err != nil {
			d.logger.Warn("discovery: skipping invalid provider",
				"provider", p.ID, "error", err)
			continue
		}
		valid = append(valid, p)
	}

	if len(valid) > maxDiscoveredProviders {
		d.logger.Error("discovery: too many providers, truncating",
			"count", len(valid), "max", maxDiscoveredProviders)
		valid = valid[:maxDiscoveredProviders]
	}

	current := d.providers.All()

	// Protect against empty responses removing all providers.
	if len(valid) == 0 && len(current) > 0 {
		d.logger.Warn("discovery: incoming provider list is empty, preserving current providers")
		return nil
	}

	reconciled := d.reconcile(current, valid)
	d.providers.Swap(reconciled)
	return nil
}

// reconcile merges current and incoming provider sets by ID.
// New providers are added as active. Removed providers are set to draining
// (if in-flight > 0) or dropped. Changed providers get their config updated
// while preserving status and health state.
func (d *Discovery) reconcile(current, incoming []ProviderConfig) []ProviderConfig {
	currentByID := make(map[string]ProviderConfig, len(current))
	for _, p := range current {
		currentByID[p.ID] = p
	}

	incomingByID := make(map[string]ProviderConfig, len(incoming))
	for _, p := range incoming {
		incomingByID[p.ID] = p
	}

	var result []ProviderConfig

	// Process incoming: add new or update existing.
	for _, inc := range incoming {
		if cur, exists := currentByID[inc.ID]; exists {
			// Preserve status from current if incoming doesn't specify one.
			if inc.Status == "" {
				inc.Status = cur.Status
			}
			result = append(result, inc)
		} else {
			// New provider.
			if inc.Status == "" {
				inc.Status = ProviderStatusActive
			}
			d.logger.Info("discovery: adding provider", "provider", inc.ID)
			result = append(result, inc)
		}
	}

	// Process removed: keep draining providers with in-flight requests.
	for _, cur := range current {
		if _, stillPresent := incomingByID[cur.ID]; stillPresent {
			continue
		}
		if d.health != nil && d.health.Inflight(cur.ID) > 0 {
			cur.Status = ProviderStatusDraining
			d.logger.Info("discovery: draining removed provider", "provider", cur.ID,
				"inflight", d.health.Inflight(cur.ID))
			result = append(result, cur)
		} else {
			d.logger.Info("discovery: removing provider", "provider", cur.ID)
		}
	}

	return result
}
