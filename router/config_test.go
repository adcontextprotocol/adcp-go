package router

import (
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateProviderEndpoint(t *testing.T) {
	valid := []struct {
		name     string
		endpoint string
	}{
		{"public HTTPS", "https://provider.example.com/tmp"},
		{"public HTTP", "http://provider.example.com"},
		{"public IP", "https://203.0.113.1/api"},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, ValidateProviderEndpoint(tt.endpoint))
		})
	}

	invalid := []struct {
		name     string
		endpoint string
	}{
		{"localhost by name", "http://localhost:8081"},
		{"localhost uppercase", "http://LOCALHOST:8081"},
		{"loopback IPv4", "http://127.0.0.1:9000"},
		{"loopback IPv6", "http://[::1]:9000"},
		{"IPv4-mapped IPv6 private", "http://[::ffff:192.168.1.1]/api"},
		{"IPv4-mapped IPv6 loopback", "http://[::ffff:127.0.0.1]/api"},
		{"RFC-1918 10.x", "http://10.0.0.1/api"},
		{"RFC-1918 172.16.x", "http://172.16.5.4/api"},
		{"RFC-1918 192.168.x", "http://192.168.1.1/api"},
		{"link-local", "http://169.254.169.254/latest/meta-data/"},
		{"file scheme", "file:///etc/passwd"},
		{"ftp scheme", "ftp://internal-host/"},
		{"no scheme", "/relative/path"},
		{"empty", ""},
		{"no host", "http://"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, ValidateProviderEndpoint(tt.endpoint))
		})
	}
}

func TestValidateProviderConfig(t *testing.T) {
	budget := 50 * time.Millisecond

	t.Run("valid context-only provider", func(t *testing.T) {
		p := &ProviderConfig{ID: "p1", ContextMatch: true}
		assert.NoError(t, ValidateProviderConfig(p, budget))
	})

	t.Run("valid identity provider", func(t *testing.T) {
		p := &ProviderConfig{ID: "p2", IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}}
		assert.NoError(t, ValidateProviderConfig(p, budget))
	})

	t.Run("valid both match types", func(t *testing.T) {
		p := &ProviderConfig{ID: "p3", ContextMatch: true, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}}
		assert.NoError(t, ValidateProviderConfig(p, budget))
	})

	t.Run("neither context nor identity", func(t *testing.T) {
		p := &ProviderConfig{ID: "bad"}
		assert.Error(t, ValidateProviderConfig(p, budget), "expected error when neither match type is set")
	})

	t.Run("identity without countries", func(t *testing.T) {
		p := &ProviderConfig{ID: "bad", IdentityMatch: true, UIDTypes: []string{"uid2"}}
		assert.Error(t, ValidateProviderConfig(p, budget), "expected error when identity_match without countries")
	})

	t.Run("identity without uid_types", func(t *testing.T) {
		p := &ProviderConfig{ID: "bad", IdentityMatch: true, Countries: []string{"US"}}
		assert.Error(t, ValidateProviderConfig(p, budget), "expected error when identity_match without uid_types")
	})

	t.Run("timeout exceeds budget", func(t *testing.T) {
		p := &ProviderConfig{ID: "slow", ContextMatch: true, Timeout: 100 * time.Millisecond}
		assert.Error(t, ValidateProviderConfig(p, budget), "expected error when timeout exceeds latency budget")
	})

	t.Run("timeout within budget", func(t *testing.T) {
		p := &ProviderConfig{ID: "fast", ContextMatch: true, Timeout: 30 * time.Millisecond}
		assert.NoError(t, ValidateProviderConfig(p, budget))
	})

	t.Run("zero budget disables check", func(t *testing.T) {
		p := &ProviderConfig{ID: "any", ContextMatch: true, Timeout: 500 * time.Millisecond}
		assert.NoError(t, ValidateProviderConfig(p, 0))
	})

	t.Run("empty ID", func(t *testing.T) {
		p := &ProviderConfig{ID: "", ContextMatch: true}
		assert.Error(t, ValidateProviderConfig(p, budget), "expected error for empty ID")
	})

	t.Run("ID too long", func(t *testing.T) {
		long := string(make([]byte, 200))
		p := &ProviderConfig{ID: long, ContextMatch: true}
		assert.Error(t, ValidateProviderConfig(p, budget), "expected error for overly long ID")
	})

	t.Run("ID with control characters", func(t *testing.T) {
		p := &ProviderConfig{ID: "bad\x00id", ContextMatch: true}
		assert.Error(t, ValidateProviderConfig(p, budget), "expected error for ID with null byte")
	})
}

func TestEffectiveStatus(t *testing.T) {
	tests := []struct {
		status ProviderStatus
		want   ProviderStatus
	}{
		{"", ProviderStatusActive},
		{ProviderStatusActive, ProviderStatusActive},
		{ProviderStatusInactive, ProviderStatusInactive},
		{ProviderStatusDraining, ProviderStatusDraining},
	}
	for _, tt := range tests {
		p := &ProviderConfig{Status: tt.status}
		assert.Equal(t, tt.want, p.EffectiveStatus(), "EffectiveStatus(%q)", tt.status)
	}
}

func TestEffectiveTimeout(t *testing.T) {
	r := &Router{latencyBudget: 50 * time.Millisecond}

	tests := []struct {
		name            string
		providerTimeout time.Duration
		want            time.Duration
	}{
		{"zero defaults to 30ms", 0, 30 * time.Millisecond},
		{"within budget", 40 * time.Millisecond, 40 * time.Millisecond},
		{"exceeds budget clamped", 100 * time.Millisecond, 50 * time.Millisecond},
		{"equal to budget", 50 * time.Millisecond, 50 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, r.effectiveTimeout(tt.providerTimeout))
		})
	}

	t.Run("zero budget does not clamp", func(t *testing.T) {
		r2 := &Router{}
		assert.Equal(t, 100*time.Millisecond, r2.effectiveTimeout(100*time.Millisecond))
	})
}

func TestProviderConfigFromRegistrationCarriesPriority(t *testing.T) {
	registration := &tmproto.ProviderRegistration{
		ProviderID: "priority-provider",
		Endpoint:   "https://provider.example.com",
		Priority:   7,
	}

	config := ProviderConfigFromRegistration(registration)

	assert.Equal(t, 7, config.Priority)
}

func TestProviderSet_ActiveFiltersByStatus(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{
		{ID: "active", Status: ProviderStatusActive, ContextMatch: true},
		{ID: "empty-status", ContextMatch: true}, // defaults to active
		{ID: "inactive", Status: ProviderStatusInactive, ContextMatch: true},
		{ID: "draining", Status: ProviderStatusDraining, ContextMatch: true},
	})

	active := ps.Active()
	require.Len(t, active, 2)
	ids := map[string]bool{}
	for _, p := range active {
		ids[p.ID] = true
	}
	assert.True(t, ids["active"], "expected active provider")
	assert.True(t, ids["empty-status"], "expected empty-status provider")
}
