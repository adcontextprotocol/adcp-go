package identityagent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	base := func() Config {
		return Config{
			HTTPPort:                   8080,
			RequestTimeout:             40 * time.Millisecond,
			HTTPReadHeaderTimeout:      200 * time.Millisecond,
			HTTPReadTimeout:            500 * time.Millisecond,
			HTTPWriteTimeout:           1 * time.Second,
			HTTPIdleTimeout:            30 * time.Second,
			ShutdownGrace:              time.Second,
			ShutdownTimeout:            10 * time.Second,
			RequestBodyLimitBytes:      64 * 1024,
			MaxHeaderBytes:             8 * 1024,
			MaxOpenConnections:         1024,
			ResponseTTL:                60 * time.Second,
			StrictContentType:          true,
			AccessLogEnabled:           false,
			AdminPort:                  0,
			SupportedADCPMajorVersions: []int{3},
			AudienceTimeout:            10 * time.Millisecond,
			FCapTimeout:                10 * time.Millisecond,
			IdentityConfig: IdentityConfigSourceConfig{
				URL:                "https://config.example/",
				Token:              "tok",
				RefreshInterval:    time.Minute,
				StartMode:          StartModeRetry,
				StartRetryDeadline: time.Minute,
			},
			TMP: TMPConfig{
				RegistryURL:    "https://registry.example/snapshot",
				OwnEndpointURL: "https://self.example/identity",
			},
			FCapValkey: ValkeyBlock{
				Enabled: true,
				Mode:    "standalone",
				Shards:  map[string]string{"0": "h:1"},
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
			name:    "non-positive shutdown timeout",
			mutate:  func(c *Config) { c.ShutdownTimeout = 0 },
			wantErr: "SHUTDOWN_TIMEOUT",
		},
		{
			name:    "non-positive read-header timeout",
			mutate:  func(c *Config) { c.HTTPReadHeaderTimeout = 0 },
			wantErr: "HTTP_READ_HEADER_TIMEOUT",
		},
		{
			name: "read-header timeout exceeds read timeout",
			mutate: func(c *Config) {
				c.HTTPReadHeaderTimeout = 2 * time.Second
				c.HTTPReadTimeout = 1 * time.Second
			},
			wantErr: "must be <= HTTP_READ_TIMEOUT",
		},
		{
			name:    "non-positive write timeout",
			mutate:  func(c *Config) { c.HTTPWriteTimeout = 0 },
			wantErr: "HTTP_WRITE_TIMEOUT",
		},
		{
			name: "write timeout does not exceed request timeout",
			mutate: func(c *Config) {
				c.RequestTimeout = 1 * time.Second
				c.HTTPWriteTimeout = 1 * time.Second
			},
			wantErr: "must be greater than REQUEST_TIMEOUT",
		},
		{
			name:    "non-positive idle timeout",
			mutate:  func(c *Config) { c.HTTPIdleTimeout = 0 },
			wantErr: "HTTP_IDLE_TIMEOUT",
		},
		{
			name:    "non-positive body limit",
			mutate:  func(c *Config) { c.RequestBodyLimitBytes = 0 },
			wantErr: "REQUEST_BODY_LIMIT_BYTES",
		},
		{
			name:    "non-positive max header bytes",
			mutate:  func(c *Config) { c.MaxHeaderBytes = 0 },
			wantErr: "MAX_HEADER_BYTES",
		},
		{
			name:    "non-positive max open connections",
			mutate:  func(c *Config) { c.MaxOpenConnections = 0 },
			wantErr: "MAX_OPEN_CONNECTIONS",
		},
		{
			name:    "admin port out of range",
			mutate:  func(c *Config) { c.AdminPort = 99999 },
			wantErr: "ADMIN_PORT",
		},
		{
			name: "admin port equals http port",
			mutate: func(c *Config) {
				c.AdminPort = c.HTTPPort
			},
			wantErr: "must differ from HTTP_PORT",
		},
		{
			name: "admin port set and distinct ok",
			mutate: func(c *Config) {
				c.AdminPort = c.HTTPPort + 1
			},
		},
		{
			name:    "non-positive response ttl",
			mutate:  func(c *Config) { c.ResponseTTL = 0 },
			wantErr: "RESPONSE_TTL",
		},
		{
			name:    "response ttl exceeds serve window max",
			mutate:  func(c *Config) { c.ResponseTTL = 301 * time.Second },
			wantErr: "RESPONSE_TTL must be <= 300s",
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
			name:    "unknown start mode",
			mutate:  func(c *Config) { c.IdentityConfig.StartMode = "bogus" },
			wantErr: "CONFIG_START_MODE",
		},
		{
			name: "retry mode requires deadline",
			mutate: func(c *Config) {
				c.IdentityConfig.StartMode = StartModeRetry
				c.IdentityConfig.StartRetryDeadline = 0
			},
			wantErr: "CONFIG_START_RETRY_DEADLINE",
		},
		{
			name: "fail-fast ignores deadline",
			mutate: func(c *Config) {
				c.IdentityConfig.StartMode = StartModeFailFast
				c.IdentityConfig.StartRetryDeadline = 0
			},
		},
		{
			name: "best-effort ignores deadline",
			mutate: func(c *Config) {
				c.IdentityConfig.StartMode = StartModeBestEffort
				c.IdentityConfig.StartRetryDeadline = 0
			},
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
			name: "tmpx full ok",
			mutate: func(c *Config) {
				c.TMPX.EncryptJWKSURL = "https://jwks.example"
				c.TMPX.Country = "US"
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
		{
			name:    "empty supported major versions",
			mutate:  func(c *Config) { c.SupportedADCPMajorVersions = nil },
			wantErr: "SUPPORTED_ADCP_MAJOR_VERSIONS",
		},
		{
			name:    "supported major version out of range",
			mutate:  func(c *Config) { c.SupportedADCPMajorVersions = []int{100} },
			wantErr: "out of range",
		},
		{
			name:   "multiple supported major versions ok",
			mutate: func(c *Config) { c.SupportedADCPMajorVersions = []int{2, 3} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadConfigFromEnv_ExtraHeaders(t *testing.T) {
	// Minimal env required by LoadConfigFromEnv to produce a populated Config.
	t.Setenv("CONFIG_SOURCE_URL", "https://config.example/")
	t.Setenv("CONFIG_SOURCE_TOKEN", "tok")

	t.Run("unset yields nil map", func(t *testing.T) {
		cfg, err := LoadConfigFromEnv()
		require.NoError(t, err)
		assert.Nil(t, cfg.IdentityConfig.ExtraHeaders)
	})

	t.Run("valid JSON object is parsed", func(t *testing.T) {
		t.Setenv("CONFIG_SOURCE_EXTRA_HEADERS", `{"X-Custom-A":"alpha","X-Custom-B":"beta"}`)
		cfg, err := LoadConfigFromEnv()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"X-Custom-A": "alpha",
			"X-Custom-B": "beta",
		}, cfg.IdentityConfig.ExtraHeaders)
	})

	t.Run("invalid JSON surfaces as error", func(t *testing.T) {
		t.Setenv("CONFIG_SOURCE_EXTRA_HEADERS", `not-json`)
		_, err := LoadConfigFromEnv()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CONFIG_SOURCE_EXTRA_HEADERS")
	})

	t.Run("empty key surfaces as error", func(t *testing.T) {
		t.Setenv("CONFIG_SOURCE_EXTRA_HEADERS", `{"":"value"}`)
		_, err := LoadConfigFromEnv()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty key")
	})
}

// TestLoadConfigFromEnv_TmpxMacroNames pins the Blocker 3 guard: the v1 spec
// caps the registered `tmpx_macros` list at TmpxMaxSlots. A permissive parser
// would emit a wire the sealer's own OpenTmpx receiver refuses to open; reject
// oversized configs at startup with a clear message.
func TestLoadConfigFromEnv_TmpxMacroNames(t *testing.T) {
	t.Setenv("CONFIG_SOURCE_URL", "https://config.example/")
	t.Setenv("CONFIG_SOURCE_TOKEN", "tok")

	t.Run("empty yields nil (legacy single-tmpx shape)", func(t *testing.T) {
		cfg, err := LoadConfigFromEnv()
		require.NoError(t, err)
		assert.Nil(t, cfg.TMPX.MacroNames)
	})

	t.Run("one entry within the cap", func(t *testing.T) {
		t.Setenv("TMPX_MACRO_NAMES", "S3_TMPX")
		cfg, err := LoadConfigFromEnv()
		require.NoError(t, err)
		assert.Equal(t, []string{"S3_TMPX"}, cfg.TMPX.MacroNames)
	})

	t.Run("two entries within the cap", func(t *testing.T) {
		t.Setenv("TMPX_MACRO_NAMES", "PIN_TMPX_1,PIN_TMPX_2")
		cfg, err := LoadConfigFromEnv()
		require.NoError(t, err)
		assert.Equal(t, []string{"PIN_TMPX_1", "PIN_TMPX_2"}, cfg.TMPX.MacroNames)
	})

	t.Run("three entries fails validation with a clear message", func(t *testing.T) {
		t.Setenv("TMPX_MACRO_NAMES", "A,B,C")
		_, err := LoadConfigFromEnv()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TMPX_MACRO_NAMES")
		assert.Contains(t, err.Error(), "exceeds")
	})
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
		assert.Equal(t, want, isValidPromName(in), "input %q", in)
	}
}
