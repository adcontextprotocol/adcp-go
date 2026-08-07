package main

import (
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGetenv returns a getenv function backed by a static map so env
// merging can be exercised without touching real process state.
func stubGetenv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// Addr resolution honors the documented precedence: flag > env > JSON.
func TestApplyEnvOverrides_AddrPrecedence(t *testing.T) {
	cases := []struct {
		name string
		flag string
		env  string
		json string
		want string
	}{
		{"flag wins over env and JSON", "flag-addr", "env-addr", "json-addr", "flag-addr"},
		{"env wins when no flag", "", "env-addr", "json-addr", "env-addr"},
		{"JSON when neither set", "", "", "json-addr", "json-addr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &router.ServerConfig{Addr: tc.json}
			applyEnvOverrides(cfg, tc.flag, stubGetenv(map[string]string{"TMP_ROUTER_ADDR": tc.env}))
			assert.Equal(t, tc.want, cfg.Addr)
		})
	}
}

// All TMP_ROUTER_SIGNING_* env vars override JSON values.
func TestApplyEnvOverrides_SigningLayering(t *testing.T) {
	cfg := &router.ServerConfig{
		Signing: router.SigningConfig{
			KeyID:          "json-kid",
			PrivateKeyPath: "/json/key",
			PropertyRIDs:   []string{"json-rid"},
		},
	}
	applyEnvOverrides(cfg, "", stubGetenv(map[string]string{
		"TMP_ROUTER_SIGNING_KID":           "env-kid",
		"TMP_ROUTER_SIGNING_KEY_PATH":      "/env/key",
		"TMP_ROUTER_SIGNING_PROPERTY_RIDS": "rid-a, rid-b",
		"TMP_ROUTER_SIGNING_DISABLED":      "true",
	}))
	assert.Equal(t, "env-kid", cfg.Signing.KeyID)
	assert.Equal(t, "/env/key", cfg.Signing.PrivateKeyPath)
	assert.Equal(t, []string{"rid-a", "rid-b"}, cfg.Signing.PropertyRIDs)
	assert.True(t, cfg.Signing.Disabled)
}

// TLS env vars flow through and Validate then catches half-config —
// covering the layering path the reviewer flagged as untested on PR-A.
func TestApplyEnvOverrides_TLSHalfConfigCaughtAfterMerge(t *testing.T) {
	cfg := &router.ServerConfig{
		TLS: router.TLSConfig{CertPath: "/json/cert"}, // JSON has cert but no key
	}
	applyEnvOverrides(cfg, "", stubGetenv(nil))
	// After merge, only the cert path is set → Validate must fail.
	assert.Error(t, cfg.TLS.Validate())

	// Now supply the missing key via env — Validate should pass.
	applyEnvOverrides(cfg, "", stubGetenv(map[string]string{
		"TMP_ROUTER_TLS_KEY": "/env/key",
	}))
	assert.NoError(t, cfg.TLS.Validate())
	assert.Equal(t, "/json/cert", cfg.TLS.CertPath)
	assert.Equal(t, "/env/key", cfg.TLS.KeyPath)
}

// Registry env vars populate the config and PollInterval() honors the
// override.
func TestApplyEnvOverrides_Registry(t *testing.T) {
	cfg := &router.ServerConfig{}
	applyEnvOverrides(cfg, "", stubGetenv(map[string]string{
		"TMP_ROUTER_REGISTRY_FEED_URL":          "https://registry.example/",
		"TMP_ROUTER_REGISTRY_FEED_TOKEN":        "t0k",
		"TMP_ROUTER_REGISTRY_POLL_INTERVAL_SEC": "45",
		"TMP_ROUTER_REGISTRY_BOOTSTRAP_LIMIT":   "5000",
		"TMP_ROUTER_REGISTRY_FEED_LIMIT":        "500",
	}))
	assert.True(t, cfg.Registry.Enabled())
	assert.Equal(t, "https://registry.example/", cfg.Registry.FeedURL)
	assert.Equal(t, "t0k", cfg.Registry.FeedToken)
	assert.Equal(t, 45*time.Second, cfg.Registry.PollInterval())
	assert.Equal(t, 5000, cfg.Registry.BootstrapLimit)
	assert.Equal(t, 500, cfg.Registry.FeedLimit)
}

// Cache env vars toggle the Context Match cache and override the
// fallback TTL. Both override any JSON-supplied values.
func TestApplyEnvOverrides_Cache(t *testing.T) {
	cfg := &router.ServerConfig{
		Cache: router.CacheConfig{DefaultTTLSeconds: 60},
	}
	applyEnvOverrides(cfg, "", stubGetenv(map[string]string{
		"TMP_ROUTER_CACHE_DISABLED":        "true",
		"TMP_ROUTER_CACHE_DEFAULT_TTL_SEC": "300",
	}))
	assert.True(t, cfg.Cache.Disabled)
	assert.Equal(t, 300, cfg.Cache.DefaultTTLSeconds)
	assert.Equal(t, 5*time.Minute, cfg.Cache.DefaultTTL())

	// Only the disabled flag flips when the TTL var is absent.
	cfg2 := &router.ServerConfig{Cache: router.CacheConfig{DefaultTTLSeconds: 60}}
	applyEnvOverrides(cfg2, "", stubGetenv(map[string]string{"TMP_ROUTER_CACHE_DISABLED": "1"}))
	assert.True(t, cfg2.Cache.Disabled)
	assert.Equal(t, 60, cfg2.Cache.DefaultTTLSeconds, "JSON TTL preserved when env doesn't override")
}

// A non-numeric TTL env var is ignored so a typo doesn't collapse the
// JSON-configured value to zero (which would then be filled by the
// 5-minute default — subtle regression). Mirrors the registry
// bad-number handling.
func TestApplyEnvOverrides_CacheIgnoresBadTTL(t *testing.T) {
	cfg := &router.ServerConfig{Cache: router.CacheConfig{DefaultTTLSeconds: 60}}
	applyEnvOverrides(cfg, "", stubGetenv(map[string]string{
		"TMP_ROUTER_CACHE_DEFAULT_TTL_SEC": "not-a-number",
	}))
	assert.Equal(t, 60, cfg.Cache.DefaultTTLSeconds, "bad value must not clobber JSON")
}

// Non-numeric limit env vars are ignored so a typo doesn't zero out the
// JSON-configured value. Matches how the existing signing/TLS blocks
// silently ignore unset vars.
func TestApplyEnvOverrides_RegistryIgnoresBadNumbers(t *testing.T) {
	cfg := &router.ServerConfig{
		Registry: router.RegistryConfig{
			PollIntervalSeconds: 60,
			BootstrapLimit:      2000,
			FeedLimit:           100,
		},
	}
	applyEnvOverrides(cfg, "", stubGetenv(map[string]string{
		"TMP_ROUTER_REGISTRY_POLL_INTERVAL_SEC": "not-a-number",
		"TMP_ROUTER_REGISTRY_BOOTSTRAP_LIMIT":   "-1",
		"TMP_ROUTER_REGISTRY_FEED_LIMIT":        "0",
	}))
	assert.Equal(t, 60, cfg.Registry.PollIntervalSeconds, "bad value must not clobber JSON")
	assert.Equal(t, 2000, cfg.Registry.BootstrapLimit)
	assert.Equal(t, 100, cfg.Registry.FeedLimit)
}

// TMP_ROUTER_ADMIN_ADDR moves the operator endpoints onto their own listener.
func TestApplyEnvOverrides_AdminAddr(t *testing.T) {
	cfg := &router.ServerConfig{Addr: ":8080"}
	assert.False(t, cfg.AdminEnabled(), "unset keeps operator endpoints on the main listener")

	applyEnvOverrides(cfg, "", stubGetenv(map[string]string{"TMP_ROUTER_ADMIN_ADDR": "127.0.0.1:9090"}))
	assert.Equal(t, "127.0.0.1:9090", cfg.AdminAddr)
	assert.True(t, cfg.AdminEnabled())
	assert.NoError(t, cfg.Validate())
}

// A shared address would silently collapse the split, putting /providers back on
// the public listener.
func TestServerConfigValidate_AdminAddrMustDiffer(t *testing.T) {
	cfg := &router.ServerConfig{
		Addr:      ":8080",
		AdminAddr: ":8080",
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must differ from addr")

	cfg.AdminAddr = ":9090"
	assert.NoError(t, cfg.Validate())
}
