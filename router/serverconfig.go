package router

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

// ServerConfig is the JSON config file format for the router.
type ServerConfig struct {
	Addr            string            `json:"addr"`
	LatencyBudgetMs int               `json:"latency_budget_ms"`
	Providers       []ProviderConfig  `json:"providers"`
	Health          HealthConfig      `json:"health"`
	HealthCheck     HealthCheckConfig `json:"health_check"`
	Discovery       DiscoveryConfig   `json:"discovery"`
	Shutdown        ShutdownConfig    `json:"shutdown"`
	Signing         SigningConfig     `json:"signing"`
}

// SigningConfig configures the TMP request-authentication signer the router
// attaches to every provider fan-out, per the spec.
//
// Deployers MUST set KeyID and PrivateKeyPath unless Disabled is true (dev
// only). PropertyRIDs lists the registry properties this signer is authorized
// to sign for; the router publishes its public key on each listed property
// so providers can verify by looking up the property → signing keys.
type SigningConfig struct {
	KeyID          string   `json:"key_id"`
	PrivateKeyPath string   `json:"private_key_path"`
	PropertyRIDs   []string `json:"property_rids,omitempty"`
	Disabled       bool     `json:"disabled,omitempty"`
}

// LatencyBudget returns the latency budget as a time.Duration.
func (c *ServerConfig) LatencyBudget() time.Duration {
	if c.LatencyBudgetMs <= 0 {
		return 50 * time.Millisecond
	}
	return time.Duration(c.LatencyBudgetMs) * time.Millisecond
}

// HealthConfig controls circuit breaker behavior.
type HealthConfig struct {
	FailureThreshold int    `json:"failure_threshold"`
	CooldownSeconds  int    `json:"cooldown_seconds"`
}

// HealthCheckConfig controls active provider health polling.
type HealthCheckConfig struct {
	IntervalSeconds int `json:"interval_seconds"` // default 30
	TimeoutSeconds  int `json:"timeout_seconds"`  // per-check timeout, default 5
}

// ShutdownConfig controls graceful shutdown.
type ShutdownConfig struct {
	DrainSeconds int `json:"drain_seconds"`
}

// LoadServerConfig reads a JSON config file and returns the config.
// Invalid providers are logged and skipped rather than causing a hard error.
func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is from CLI flag, not user input
	if err != nil {
		return nil, err
	}
	var cfg ServerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	budget := cfg.LatencyBudget()
	valid := cfg.Providers[:0]
	for _, p := range cfg.Providers {
		if err := ValidateProviderEndpoint(p.Endpoint); err != nil {
			slog.Warn("skipping provider with invalid endpoint", "provider", p.ID, "error", err)
			continue
		}
		if err := ValidateProviderConfig(&p, budget); err != nil {
			slog.Warn("skipping invalid provider", "provider", p.ID, "error", err)
			continue
		}
		valid = append(valid, p)
	}
	cfg.Providers = valid
	return &cfg, nil
}

// DefaultServerConfig returns sensible defaults with no providers configured.
// Providers must be supplied via a config file or programmatically.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Addr:            ":8080",
		LatencyBudgetMs: 50,
		Health:          HealthConfig{FailureThreshold: 3, CooldownSeconds: 10},
		HealthCheck:     HealthCheckConfig{IntervalSeconds: 30, TimeoutSeconds: 5},
		Shutdown:        ShutdownConfig{DrainSeconds: 5},
	}
}
