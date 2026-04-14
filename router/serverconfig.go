package router

import (
	"encoding/json"
	"fmt"
	"os"
)

// ServerConfig is the JSON config file format for the router.
type ServerConfig struct {
	Addr      string           `json:"addr"`
	Providers []ProviderConfig `json:"providers"`
	Health    HealthConfig     `json:"health"`
	Shutdown  ShutdownConfig   `json:"shutdown"`
}

// HealthConfig controls circuit breaker behavior.
type HealthConfig struct {
	FailureThreshold int    `json:"failure_threshold"`
	CooldownSeconds  int    `json:"cooldown_seconds"`
}

// ShutdownConfig controls graceful shutdown.
type ShutdownConfig struct {
	DrainSeconds int `json:"drain_seconds"`
}

// LoadServerConfig reads a JSON config file and returns the config.
func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is from CLI flag, not user input
	if err != nil {
		return nil, err
	}
	var cfg ServerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	for _, p := range cfg.Providers {
		if err := ValidateProviderEndpoint(p.Endpoint); err != nil {
			return nil, fmt.Errorf("provider %q: %w", p.ID, err)
		}
	}
	return &cfg, nil
}

// DefaultServerConfig returns sensible defaults with no providers configured.
// Providers must be supplied via a config file or programmatically.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Addr:     ":8080",
		Health:   HealthConfig{FailureThreshold: 3, CooldownSeconds: 10},
		Shutdown: ShutdownConfig{DrainSeconds: 5},
	}
}
