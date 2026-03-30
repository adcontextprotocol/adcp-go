package main

import (
	"encoding/json"
	"os"
	"time"
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ServerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// DefaultServerConfig returns sensible defaults.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Addr: ":8080",
		Providers: []ProviderConfig{
			{
				ID: "reference-context", Endpoint: "http://localhost:8081",
				ContextMatch: true, WireFormats: []string{"json"},
				Timeout: 30 * time.Millisecond,
			},
			{
				ID: "reference-identity", Endpoint: "http://localhost:8082",
				IdentityMatch: true, WireFormats: []string{"json"},
				Timeout: 30 * time.Millisecond,
			},
		},
		Health:   HealthConfig{FailureThreshold: 3, CooldownSeconds: 10},
		Shutdown: ShutdownConfig{DrainSeconds: 5},
	}
}
