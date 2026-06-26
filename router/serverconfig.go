package router

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ServerConfig is the JSON/YAML config file format for the router.
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

// UnmarshalJSON accepts the top-level field names from the spec config
// sample (`router-architecture.mdx`) — `listen` for the bind address and
// `health_check_interval_sec` for the active-check interval — so a verbatim
// copy of the documented sample doesn't silently bind to the default port
// or run the health checker at the default cadence. Schema-aligned names
// take precedence when both forms appear.
func (c *ServerConfig) UnmarshalJSON(data []byte) error {
	type serverConfigAlias ServerConfig
	aux := struct {
		*serverConfigAlias
		Listen                  string `json:"listen"`
		HealthCheckIntervalSec  *int   `json:"health_check_interval_sec"`
	}{serverConfigAlias: (*serverConfigAlias)(c)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Listen != "" && c.Addr == "" {
		c.Addr = aux.Listen
	}
	if aux.HealthCheckIntervalSec != nil && c.HealthCheck.IntervalSeconds == 0 {
		c.HealthCheck.IntervalSeconds = *aux.HealthCheckIntervalSec
	}
	return nil
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

// LoadServerConfig reads a JSON or YAML config file and returns the config.
// Format is selected by file extension: .yaml/.yml use YAML, anything else
// (including .json and no extension) uses JSON. YAML is converted to JSON
// internally so all struct tags and custom UnmarshalJSON implementations on
// nested types (e.g. ProviderConfig schema-name aliases) are honored
// uniformly across formats. Invalid providers are logged and skipped rather
// than causing a hard error.
func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is from CLI flag, not user input
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		data, err = yamlToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("parse YAML config %s: %w", path, err)
		}
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

// yamlToJSON decodes YAML into a generic value, then re-encodes as JSON so a
// single json.Unmarshal pass into ServerConfig honors all struct tags and
// custom UnmarshalJSON implementations (e.g. ProviderConfig's schema-name
// aliases). Map keys arriving as interface{} from yaml.v3 are normalized to
// string keys so the JSON encoder accepts them.
func yamlToJSON(yamlBytes []byte) ([]byte, error) {
	var raw any
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return nil, err
	}
	return json.Marshal(normalizeYAMLNode(raw))
}

func normalizeYAMLNode(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[fmt.Sprint(k)] = normalizeYAMLNode(val)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeYAMLNode(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeYAMLNode(val)
		}
		return out
	default:
		return v
	}
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
