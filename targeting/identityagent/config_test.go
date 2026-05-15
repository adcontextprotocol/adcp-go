package identityagent

import (
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	base := func() Config {
		return Config{
			HTTPPort:        8080,
			RequestTimeout:  40 * time.Millisecond,
			ShutdownGrace:   time.Second,
			AudienceTimeout: 10 * time.Millisecond,
			FCapTimeout:     10 * time.Millisecond,
			IdentityConfig: IdentityConfigSourceConfig{
				URL:             "https://config.example/",
				Token:           "tok",
				RefreshInterval: time.Minute,
			},
			TMP: TMPConfig{
				RegistryURL:    "https://registry.example/snapshot",
				OwnEndpointURL: "https://self.example/tmp/identity",
			},
			FCapValkey: ValkeyBlock{
				Enabled:  true,
				Mode:     "standalone",
				Shards:   map[string]string{"0": "h:1"},
			},
		}
	}

	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{name: "ok", mutate: func(*Config) {}},
		{
			name:    "port out of range",
			mutate:  func(c *Config) { c.HTTPPort = 0 },
			wantErr: "HTTP_PORT",
		},
		{
			name:    "non-positive request timeout",
			mutate:  func(c *Config) { c.RequestTimeout = 0 },
			wantErr: "REQUEST_TIMEOUT",
		},
		{
			name:    "missing config url",
			mutate:  func(c *Config) { c.IdentityConfig.URL = "" },
			wantErr: "CONFIG_SOURCE_URL",
		},
		{
			name:    "missing config token",
			mutate:  func(c *Config) { c.IdentityConfig.Token = "" },
			wantErr: "CONFIG_SOURCE_TOKEN",
		},
		{
			name:    "fcap disabled",
			mutate:  func(c *Config) { c.FCapValkey.Enabled = false },
			wantErr: "FCAP_VALKEY",
		},
		{
			name: "tmp registry missing with signing required",
			mutate: func(c *Config) {
				c.TMP.RegistryURL = ""
			},
			wantErr: "TMP_REGISTRY_URL",
		},
		{
			name: "allow unsigned tolerates missing registry",
			mutate: func(c *Config) {
				c.TMP.AllowUnsigned = true
				c.TMP.RegistryURL = ""
				c.TMP.OwnEndpointURL = ""
			},
		},
		{
			name: "tmpx partial config",
			mutate: func(c *Config) {
				c.TMPX.EncryptJWKSURL = "https://jwks.example"
			},
			wantErr: "TMPX_COUNTRY",
		},
		{
			name: "tmpx without stub ack",
			mutate: func(c *Config) {
				c.TMPX.EncryptJWKSURL = "https://jwks.example"
				c.TMPX.Country = "US"
			},
			wantErr: "TMPX_REFERENCE_STUB_ACK",
		},
		{
			name: "tmpx full ok",
			mutate: func(c *Config) {
				c.TMPX.EncryptJWKSURL = "https://jwks.example"
				c.TMPX.Country = "US"
				c.TMPX.ReferenceStubAck = true
			},
		},
		{
			name: "metrics enabled without namespace",
			mutate: func(c *Config) {
				c.Metrics.Enabled = true
				c.Metrics.Namespace = ""
			},
			wantErr: "METRICS_NAMESPACE",
		},
		{
			name: "metrics enabled with invalid namespace",
			mutate: func(c *Config) {
				c.Metrics.Enabled = true
				c.Metrics.Namespace = "9invalid-start"
			},
			wantErr: "not a valid Prometheus name",
		},
		{
			name: "metrics enabled with valid namespace",
			mutate: func(c *Config) {
				c.Metrics.Enabled = true
				c.Metrics.Namespace = "identity_agent"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			err := cfg.Validate()
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
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestIsValidPromName(t *testing.T) {
	cases := map[string]bool{
		"identity_agent": true,
		"_under":         true,
		"abc123":         true,
		"":               false,
		"1abc":           false,
		"with-dash":      false,
		"with.dot":       false,
	}
	for in, want := range cases {
		got := isValidPromName(in)
		if got != want {
			t.Errorf("isValidPromName(%q)=%v want %v", in, got, want)
		}
	}
}
