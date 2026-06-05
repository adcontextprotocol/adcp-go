package main

import (
	"strings"
	"testing"
)

func TestRegistryConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     registryConfig
		wantErr string
	}{
		{
			name: "disabled is always valid",
			cfg:  registryConfig{Enabled: false},
		},
		{
			name: "enabled requires feed URL",
			cfg: registryConfig{
				Enabled:      true,
				PollInterval: defaultRegistryPollInterval,
				Backend:      registryBackendMemory,
			},
			wantErr: "REGISTRY_FEED_URL is required",
		},
		{
			name: "memory backend needs nothing extra",
			cfg: registryConfig{
				Enabled:      true,
				FeedURL:      "https://registry.example.com",
				PollInterval: defaultRegistryPollInterval,
				Backend:      registryBackendMemory,
			},
		},
		{
			name: "redis backend requires addr",
			cfg: registryConfig{
				Enabled:      true,
				FeedURL:      "https://registry.example.com",
				PollInterval: defaultRegistryPollInterval,
				Backend:      registryBackendRedis,
			},
			wantErr: "REGISTRY_REDIS_ADDR is required",
		},
		{
			name: "unknown backend is rejected",
			cfg: registryConfig{
				Enabled:      true,
				FeedURL:      "https://registry.example.com",
				PollInterval: defaultRegistryPollInterval,
				Backend:      "postgres",
			},
			wantErr: `REGISTRY_STORE_BACKEND "postgres" is not supported`,
		},
		{
			name: "non-positive poll interval is rejected",
			cfg: registryConfig{
				Enabled:      true,
				FeedURL:      "https://registry.example.com",
				PollInterval: 0,
				Backend:      registryBackendMemory,
			},
			wantErr: "REGISTRY_POLL_INTERVAL must be positive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
