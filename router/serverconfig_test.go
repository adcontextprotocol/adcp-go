package router

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// LoadServerConfig MUST accept the schema-aligned field names from
// docs/trusted-match/router-architecture.mdx (provider_id, timeout_ms,
// priority) so a copy-pasted config sample parses without silently dropping
// fields. The impl-internal names (id, timeout) still work for callers that
// already use them.
func TestLoadServerConfig_AcceptsSchemaFieldNames(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.json", `{
		"addr": ":8080",
		"latency_budget_ms": 50,
		"providers": [
			{
				"provider_id": "p1",
				"endpoint": "https://provider.example/agent",
				"context_match": true,
				"timeout_ms": 25,
				"priority": 10
			}
		]
	}`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Providers, 1)
	p := cfg.Providers[0]
	assert.Equal(t, "p1", p.ID, "provider_id should map to ID")
	assert.Equal(t, 25*time.Millisecond, p.Timeout, "timeout_ms should map to Timeout")
	assert.Equal(t, 10, p.Priority, "priority is captured though unused today")
}

// YAML configs MUST parse — the documented config sample is YAML.
func TestLoadServerConfig_AcceptsYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", `addr: ":8080"
latency_budget_ms: 50
providers:
  - provider_id: p1
    endpoint: https://provider.example/agent
    context_match: true
    timeout_ms: 25
`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Providers, 1)
	assert.Equal(t, "p1", cfg.Providers[0].ID)
	assert.Equal(t, 25*time.Millisecond, cfg.Providers[0].Timeout)
}

// LoadServerConfig MUST accept the top-level field names from the spec's
// sample config (`router-architecture.mdx`) — `listen` for the bind
// address and `health_check_interval_sec` for the active-check cadence —
// so a verbatim copy of the documented sample doesn't silently bind to
// the default port or run the health checker at the default interval.
func TestLoadServerConfig_AcceptsSpecTopLevelAliases(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", `listen: ":9090"
latency_budget_ms: 75
health_check_interval_sec: 7
providers:
  - provider_id: p1
    endpoint: https://provider.example/agent
    context_match: true
    timeout_ms: 25
`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.Addr, "listen should map to Addr")
	assert.Equal(t, 7, cfg.HealthCheck.IntervalSeconds, "health_check_interval_sec should map to HealthCheck.IntervalSeconds")
	require.Len(t, cfg.Providers, 1)
	assert.Equal(t, "p1", cfg.Providers[0].ID, "provider_id should still map to ID")
}

// Existing impl-internal names continue to work — backward compat.
func TestLoadServerConfig_AcceptsImplFieldNames(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.json", `{
		"addr": ":8080",
		"providers": [
			{
				"id": "p1",
				"endpoint": "https://provider.example/agent",
				"context_match": true,
				"timeout": 25000000
			}
		]
	}`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Providers, 1)
	assert.Equal(t, "p1", cfg.Providers[0].ID)
	assert.Equal(t, 25*time.Millisecond, cfg.Providers[0].Timeout)
}
