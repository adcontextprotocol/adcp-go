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
	Addr string `json:"addr"`
	// AdminAddr, when set, moves the operator endpoints (/metrics, /providers)
	// onto a second listener. Empty keeps them on the main listener, which is
	// the pre-existing behavior. Mirrors ADMIN_PORT on the identity and context
	// agents; /healthz stays on the protocol listener either way because that
	// is the address load balancers probe.
	AdminAddr       string            `json:"admin_addr"`
	LatencyBudgetMs int               `json:"latency_budget_ms"`
	Providers       []ProviderConfig  `json:"providers"`
	Health          HealthConfig      `json:"health"`
	HealthCheck     HealthCheckConfig `json:"health_check"`
	Discovery       DiscoveryConfig   `json:"discovery"`
	Shutdown        ShutdownConfig    `json:"shutdown"`
	Signing         SigningConfig     `json:"signing"`
	Auth            AuthConfig        `json:"auth"`
	TLS             TLSConfig         `json:"tls"`
	Registry        RegistryConfig    `json:"registry"`
	Cache           CacheConfig       `json:"cache"`
}

// AdminEnabled reports whether the operator endpoints should be served on a
// separate listener.
func (c *ServerConfig) AdminEnabled() bool { return strings.TrimSpace(c.AdminAddr) != "" }

// Validate checks the config's cross-field invariants. Call this after all
// override layers (flags, env, file) have been applied — individual sections
// cannot see each other, and the mTLS rule below spans two of them.
func (c *ServerConfig) Validate() error {
	if err := c.TLS.Validate(); err != nil {
		return err
	}
	if err := c.Auth.Validate(); err != nil {
		return err
	}
	// mTLS needs the router to terminate TLS: the client-cert trust anchor is
	// installed on the router's own listener. With TLS terminated upstream the
	// router never sees a peer certificate, so every request would be rejected
	// for a missing client cert — fail at startup instead.
	if c.Auth.ClientCAPath != "" && !c.TLS.Enabled() {
		return fmt.Errorf("auth: client_ca_path requires the router to terminate TLS — set tls.cert and tls.key, or authenticate with auth.api_keys instead")
	}
	// Sharing the address would silently collapse the split the admin listener
	// exists to create, putting /providers back on the public port.
	if c.AdminEnabled() && strings.TrimSpace(c.AdminAddr) == strings.TrimSpace(c.Addr) {
		return fmt.Errorf("admin_addr (%q) must differ from addr — the operator endpoints would otherwise stay on the public listener", c.AdminAddr)
	}
	return nil
}

// CacheConfig controls the per-provider Context Match response cache
// (spec §Caching). When Disabled is false, the router runs a cache
// with DefaultTTL applied on every entry whose provider does not
// specify a cache_ttl override.
type CacheConfig struct {
	// Disabled turns the cache off entirely — the router fans out on
	// every request. Useful for A/B comparisons and for operators
	// terminating cache at an upstream layer.
	Disabled bool `json:"disabled,omitempty"`
	// DefaultTTLSeconds sets the fallback TTL when a provider response
	// omits cache_ttl. Spec recommends 5 minutes; zero or unset
	// collapses to that default.
	DefaultTTLSeconds int `json:"default_ttl_seconds,omitempty"`
	// MaxEntries caps the number of live cache entries so a caller
	// varying placement/seller/country cannot grow the map without
	// bound. Zero or unset applies DefaultContextCacheMaxEntries.
	MaxEntries int `json:"max_entries,omitempty"`
}

// DefaultContextCacheMaxEntries is the fallback cap when the operator
// does not configure MaxEntries. Sized well above any realistic
// {property_rid × placement × provider × seller × country} product
// for a real deployment.
const DefaultContextCacheMaxEntries = 10_000

// MaxEntriesResolved returns the cap the cache should apply, honoring
// the operator's override when set and falling back to the default
// otherwise.
func (c CacheConfig) MaxEntriesResolved() int {
	if c.MaxEntries > 0 {
		return c.MaxEntries
	}
	return DefaultContextCacheMaxEntries
}

// DefaultTTL returns the fallback TTL as a duration, honoring the
// spec's 5-minute recommendation when unset. Zero or negative values
// yield DefaultContextCacheTTL.
func (c CacheConfig) DefaultTTL() time.Duration {
	if c.DefaultTTLSeconds <= 0 {
		return DefaultContextCacheTTL
	}
	return time.Duration(c.DefaultTTLSeconds) * time.Second
}

// RegistryConfig points the router at the AdCP registry feed so it can serve
// real property metadata from `/registry/snapshot` instead of a stub. Left
// empty, the router falls back to seeding only the property RIDs it is
// authorized to sign for (dev-mode default).
//
// The feed endpoint is the same one the reference context/identity agents
// consume: `${feed_url}/api/registry/feed` with bearer authentication. The
// router keeps the resulting property index in memory only — the router is
// stateless by design (see docs/trusted-match/router-architecture.mdx) so no
// persistent backend is exposed here.
type RegistryConfig struct {
	FeedURL             string `json:"feed_url"`
	FeedToken           string `json:"feed_token,omitempty"`
	PollIntervalSeconds int    `json:"poll_interval_seconds,omitempty"`
	BootstrapLimit      int    `json:"bootstrap_limit,omitempty"`
	FeedLimit           int    `json:"feed_limit,omitempty"`
}

// Enabled reports whether the router should stand up a registry feed sync.
func (r RegistryConfig) Enabled() bool { return r.FeedURL != "" }

// PollInterval returns the poll interval as a duration, falling back to 30s
// when unset. Matches the default in the internal `registry` package so a
// router operator gets the same behavior as the reference agents.
func (r RegistryConfig) PollInterval() time.Duration {
	if r.PollIntervalSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(r.PollIntervalSeconds) * time.Second
}

// TLSConfig configures TLS termination in the router binary. When both
// CertPath and KeyPath are set, the router serves HTTPS. When both are empty,
// the router serves cleartext HTTP (typical when TLS is terminated by an
// upstream ingress/load balancer). Setting only one is a configuration error
// and Validate reports it.
//
// The field names `cert` and `key` match the top-level `tls:` block in the
// spec's sample deployment YAML (docs/trusted-match/router-architecture.mdx).
type TLSConfig struct {
	CertPath string `json:"cert"`
	KeyPath  string `json:"key"`
}

// Enabled reports whether TLS should be enabled based on the presence of both
// cert and key paths.
func (t TLSConfig) Enabled() bool {
	return t.CertPath != "" && t.KeyPath != ""
}

// Validate reports a configuration error when exactly one of cert/key is set,
// which almost always indicates a missing env var or a typo the operator would
// rather find at startup than after their next cert rotation.
func (t TLSConfig) Validate() error {
	if (t.CertPath == "") != (t.KeyPath == "") {
		return fmt.Errorf("tls: both cert and key must be set together (cert=%q, key=%q)", t.CertPath, t.KeyPath)
	}
	return nil
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
		Listen                 string `json:"listen"`
		HealthCheckIntervalSec *int   `json:"health_check_interval_sec"`
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
	FailureThreshold int `json:"failure_threshold"`
	CooldownSeconds  int `json:"cooldown_seconds"`
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
