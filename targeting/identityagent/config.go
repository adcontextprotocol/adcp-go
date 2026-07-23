// Package identityagent assembles a production-ready TMP identity-match
// service. It composes the audience-only IdentityEngine from the parent
// targeting package with a frequency-cap gate, surfaces them behind
// TMP-signature verification, exports Prometheus-compatible metrics via the
// OpenTelemetry meter provider, and orchestrates a coordinated graceful
// shutdown.
package identityagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Config is the env-derived configuration for a single identity-agent
// process. Build with LoadConfigFromEnv and inspect with Validate before
// passing to Run.
type Config struct {
	HTTPPort       int
	RequestTimeout time.Duration

	// HTTP server timeouts. RequestTimeout is the per-request handler-internal
	// budget enforced via context.WithTimeout; the four below are outer
	// listener-level bounds that absorb slow-client behavior and shut the
	// door on slow-header / slow-body / slow-read attacks.
	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration

	// ShutdownGrace is the readiness-flip propagation window; ShutdownTimeout
	// is the hard ceiling on graceful shutdown after that window expires.
	// Together they must fit inside the orchestrator's terminationGracePeriod.
	ShutdownGrace   time.Duration
	ShutdownTimeout time.Duration

	// RequestBodyLimitBytes is the maximum POST /identity body size in
	// bytes. Anything larger is rejected at decode time.
	RequestBodyLimitBytes int

	// MaxHeaderBytes caps the total size of request headers. Goes onto
	// http.Server.MaxHeaderBytes; tighter than Go's 1 MiB default to
	// reject malformed bloat early.
	MaxHeaderBytes int

	// MaxOpenConnections caps the number of concurrently-accepted TCP
	// connections to the listener. New SYNs queue in the kernel backlog
	// once the cap is reached and are eventually rejected. Pair with the
	// container's file-descriptor ulimit.
	MaxOpenConnections int

	// ResponseTTL is the cache TTL hint returned to callers in
	// IdentityMatchResponse.ServeWindowSec.
	ResponseTTL time.Duration

	// StrictContentType rejects requests whose Content-Type is not
	// application/json with 415 Unsupported Media Type. Enabled by default
	// to close content-type confusion attacks; disable only for legacy
	// callers that send a different type.
	StrictContentType bool

	// AccessLogEnabled emits one structured INFO log line per request
	// (method, path, status, bytes, latency, request_id). Off by default
	// to avoid log-volume cost at 10k QPS; useful in staging or for
	// incident triage.
	AccessLogEnabled bool

	// AdminPort, when > 0, moves /metrics, /live, and /debug/pprof onto a
	// second HTTP listener on that port. /identity and /health stay on
	// HTTPPort — both are part of the TMP protocol surface that publisher
	// routers probe externally, so they MUST share the listener serving
	// /identity. When 0 (default) all endpoints share HTTPPort. Splitting
	// lets ops apply different network policies to observability vs the
	// public attack surface.
	AdminPort int

	// SupportedADCPMajorVersions enumerates the AdCP major versions this
	// agent accepts on inbound `adcp_major_version`. Requests carrying an
	// unsupported value are rejected with HTTP 400. Empty means "accept any
	// value" — but Validate rejects empty, so deployments must declare
	// support explicitly.
	SupportedADCPMajorVersions []int

	LogLevel string

	TMP            TMPConfig
	TMPX           TMPXConfig
	LiveRamp       LiveRampSidecarConfig
	IdentityConfig IdentityConfigSourceConfig
	AudienceValkey ValkeyBlock
	FCapValkey     ValkeyBlock

	// FallbackFCapValkey / FallbackAudienceValkey, when Enabled, wrap the
	// primary store with a union-read adapter used during a Valkey
	// resharding: reads OR the primary (new topology) with the fallback
	// (old topology) so answers stay correct while the reshard is in
	// flight. Writes always go to the primary. Both blocks default off;
	// the resharding runbook is the only expected caller.
	FallbackFCapValkey     ValkeyBlock
	FallbackAudienceValkey ValkeyBlock

	AudienceTimeout time.Duration
	FCapTimeout     time.Duration

	Metrics MetricsConfig
	Pprof   PprofConfig
}

// TMPConfig drives TMP signature verification on /identity.
type TMPConfig struct {
	// RegistryURL is where the agent resolves publisher signing keys.
	// Meaning depends on RegistryMode:
	//   - "snapshot" (default): a router's /registry/snapshot endpoint
	//     that returns {properties[]{signing_keys[]}}. Bulk-synced.
	//   - "authorization": an AdCP registry authorizations endpoint
	//     (e.g. https://agenticadvertising.org/api/registry/authorizations)
	//     that the agent queries per-request using
	//     `?agent_url=<seller_agent_url>`.
	RegistryURL string
	// RegistryMode selects the KeyStore implementation. "snapshot" (default)
	// preserves back-compat with existing deployments. "authorization"
	// switches to per-agent lazy resolution — the right choice when the
	// verifier does not know its callers ahead of time.
	RegistryMode string
	// RegistryBearer is sent as `Authorization: Bearer <token>` on
	// authorization-mode registry calls. Ignored in snapshot mode.
	RegistryBearer string
	OwnEndpointURL string
	AllowUnsigned  bool
}

// Registry mode enum values.
const (
	RegistryModeSnapshot      = "snapshot"
	RegistryModeAuthorization = "authorization"
)

// TMPXConfig drives TMPX response sealing. Disabled when EncryptJWKSURL is
// empty.
type TMPXConfig struct {
	EncryptJWKSURL string
	EncryptJWKSTTL time.Duration
	Country        string
	Priority       string

	// MacroNames is the ordered list of ad-server macro slot names this
	// provider's TMPX response fills, matching the provider's registered
	// `tmpx_macros` (provider-registration.json). The sealer pairs the
	// sealed token with these names to populate IdentityMatchResponse's
	// `tmpx_macros[]`. When empty the response carries only the legacy
	// singular `tmpx` field (deprecated, removed in 4.0). The v1 spec
	// caps the registered list at 2 entries — enforced at registration
	// time, not here. When more than one name is configured the sealed
	// token is split into up to len(MacroNames) chunks of at most 255
	// bytes each (the GAM `%%PATTERN_MACRO%%` substitution limit), one
	// per slot in the configured order.
	MacroNames []string
}

// LiveRampSidecarConfig optionally enables calls to the Scope3 LiveRamp
// mapping sidecar for decoding RampID and RampID-derived identities into
// the binary form TMPX expects.
//
// When URL is empty the sidecar is disabled: any RampID arriving on
// /identity is silently dropped from the TMPX wire (other UID types are
// unaffected). Timeout and DialTimeout default to 2s / 1s respectively
// when zero. The sidecar is assumed to be reachable in the same network
// trust boundary as the agent, so no auth is sent.
type LiveRampSidecarConfig struct {
	URL         string
	Timeout     time.Duration
	DialTimeout time.Duration
}

// Enabled reports whether a LiveRamp sidecar URL was configured.
func (c LiveRampSidecarConfig) Enabled() bool { return c.URL != "" }

// IdentityConfigSourceConfig drives the Scope3 identity-config refresh
// service.
//
// StartMode controls behavior when the initial LoadAll fails:
//   - "retry"       (default) — block startup retrying until StartRetryDeadline
//   - "fail-fast"             — exit on the first failure
//   - "best-effort"           — start with an empty snapshot; rely on the
//     normal refresh tick to populate
//
// StartRetryDeadline bounds the StartMode=retry retry loop. Ignored for
// the other modes. A pod whose config source is down for longer than this
// will exit; pick a value that matches your upstream's recovery SLO.
type IdentityConfigSourceConfig struct {
	URL                string
	Token              string
	Timeout            time.Duration
	RefreshInterval    time.Duration
	StartMode          string
	StartRetryDeadline time.Duration

	// ExtraHeaders are added to every outbound config-source request on top
	// of the headers the source manages itself (Authorization, Content-Type,
	// Accept). Loaded from CONFIG_SOURCE_EXTRA_HEADERS as a JSON object of
	// name→value pairs. Collisions with the managed headers are rejected by
	// the source constructor.
	ExtraHeaders map[string]string
}

// ValkeyBlock is the per-backend Valkey configuration. Use ToRedisStoreConfig
// to project onto the redisstore.Config the Build helper consumes.
//
// The identity-agent only issues read commands (HEXISTS) against Valkey;
// fcap writes come from the frequency-writer and audience writes from a
// separate audience-writer. WriteTimeout is intentionally not exposed.
type ValkeyBlock struct {
	Enabled bool

	Mode     string
	Shards   map[string]string
	Username string
	Password string
	DB       int
	TLS      bool

	DialTimeout time.Duration
	ReadTimeout time.Duration
	PoolSize    int
}

// ToRedisStoreConfig projects onto the redisstore.Config the Build helper
// consumes. Only meaningful when Enabled is true; the caller is responsible
// for gating the call (the identityagent.Config.Validate flow does this).
func (b ValkeyBlock) ToRedisStoreConfig() redisstore.Config {
	return redisstore.Config{
		Mode:        redisstore.Mode(b.Mode),
		Shards:      b.Shards,
		Username:    b.Username,
		Password:    b.Password,
		DB:          b.DB,
		TLS:         b.TLS,
		DialTimeout: b.DialTimeout,
		ReadTimeout: b.ReadTimeout,
		PoolSize:    b.PoolSize,
	}
}

// MetricsConfig drives the Prometheus exporter and /metrics endpoint.
type MetricsConfig struct {
	Enabled   bool
	Namespace string
}

// PprofConfig toggles /debug/pprof on the observability mux.
type PprofConfig struct {
	Enabled bool
}

const (
	defaultHTTPPort = 8080

	// 40ms per-request internal budget. The four HTTP server timeouts below
	// are outer slow-client absorbing bounds; tightened from the values used
	// when the listener didn't have a per-request timeout above it.
	defaultRequestTimeout        = 40 * time.Millisecond
	defaultHTTPReadHeaderTimeout = 200 * time.Millisecond
	defaultHTTPReadTimeout       = 500 * time.Millisecond
	defaultHTTPWriteTimeout      = 1 * time.Second
	defaultHTTPIdleTimeout       = 30 * time.Second
	defaultShutdownGrace         = 1 * time.Second
	defaultShutdownTimeout       = 10 * time.Second
	defaultRequestBodyLimitBytes = 64 * 1024
	defaultMaxHeaderBytes        = 8 * 1024
	defaultMaxOpenConnections    = 1024
	defaultResponseTTL           = 60 * time.Second
	defaultStrictContentType     = true
	defaultAccessLogEnabled      = false
	defaultAdminPort             = 0
	defaultLogLevel              = "info"
	defaultJWKSTTL               = 5 * time.Minute
	defaultConfigTimeout         = 30 * time.Second
	defaultRefreshInterval       = 5 * time.Minute
	defaultStartMode             = "retry"
	defaultStartRetryDeadline    = 5 * time.Minute
	defaultAudienceTimeout       = 10 * time.Millisecond
	defaultFCapTimeout           = 10 * time.Millisecond
	defaultNamespace             = "identity_agent"
)

// defaultSupportedADCPMajorVersions is the AdCP major-version set the agent
// accepts when SUPPORTED_ADCP_MAJOR_VERSIONS is unset. Tracks the latest
// stable major surface.
var defaultSupportedADCPMajorVersions = []int{3}

// Valid CONFIG_START_MODE values. Exported so callers and tests can refer
// to them by name rather than literal strings.
const (
	StartModeRetry      = "retry"
	StartModeFailFast   = "fail-fast"
	StartModeBestEffort = "best-effort"
)

// LoadConfigFromEnv reads every recognized environment variable into a Config.
// Validation is the caller's responsibility — call Validate before using the
// result. Defaults are applied for unset optional fields so the returned
// Config is directly usable when only the required vars are set.
func LoadConfigFromEnv() (Config, error) {
	var errs []error

	port, err := lookupInt("HTTP_PORT", defaultHTTPPort)
	if err != nil {
		errs = append(errs, err)
	}
	reqTimeout, err := lookupDuration("REQUEST_TIMEOUT", defaultRequestTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	shutdownGrace, err := lookupDuration("SHUTDOWN_GRACE", defaultShutdownGrace)
	if err != nil {
		errs = append(errs, err)
	}
	shutdownTimeout, err := lookupDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	readHeaderTimeout, err := lookupDuration("HTTP_READ_HEADER_TIMEOUT", defaultHTTPReadHeaderTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	readTimeout, err := lookupDuration("HTTP_READ_TIMEOUT", defaultHTTPReadTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	writeTimeout, err := lookupDuration("HTTP_WRITE_TIMEOUT", defaultHTTPWriteTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	idleTimeout, err := lookupDuration("HTTP_IDLE_TIMEOUT", defaultHTTPIdleTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	bodyLimit, err := lookupInt("REQUEST_BODY_LIMIT_BYTES", defaultRequestBodyLimitBytes)
	if err != nil {
		errs = append(errs, err)
	}
	maxHeader, err := lookupInt("MAX_HEADER_BYTES", defaultMaxHeaderBytes)
	if err != nil {
		errs = append(errs, err)
	}
	maxConns, err := lookupInt("MAX_OPEN_CONNECTIONS", defaultMaxOpenConnections)
	if err != nil {
		errs = append(errs, err)
	}
	strictCT, err := lookupBool("STRICT_CONTENT_TYPE", defaultStrictContentType)
	if err != nil {
		errs = append(errs, err)
	}
	accessLog, err := lookupBool("ACCESS_LOG_ENABLED", defaultAccessLogEnabled)
	if err != nil {
		errs = append(errs, err)
	}
	adminPort, err := lookupInt("ADMIN_PORT", defaultAdminPort)
	if err != nil {
		errs = append(errs, err)
	}
	responseTTL, err := lookupDuration("RESPONSE_TTL", defaultResponseTTL)
	if err != nil {
		errs = append(errs, err)
	}
	allowUnsigned, err := lookupBool("TMP_ALLOW_UNSIGNED", false)
	if err != nil {
		errs = append(errs, err)
	}
	jwksTTL, err := lookupDuration("TMPX_ENCRYPT_JWKS_TTL", defaultJWKSTTL)
	if err != nil {
		errs = append(errs, err)
	}
	lrTimeout, err := lookupDuration("LIVERAMP_SIDECAR_TIMEOUT", 0)
	if err != nil {
		errs = append(errs, err)
	}
	lrDialTimeout, err := lookupDuration("LIVERAMP_SIDECAR_DIAL_TIMEOUT", 0)
	if err != nil {
		errs = append(errs, err)
	}
	cfgTimeout, err := lookupDuration("CONFIG_SOURCE_TIMEOUT", defaultConfigTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	refreshInterval, err := lookupDuration("CONFIG_REFRESH_INTERVAL", defaultRefreshInterval)
	if err != nil {
		errs = append(errs, err)
	}
	startRetryDeadline, err := lookupDuration("CONFIG_START_RETRY_DEADLINE", defaultStartRetryDeadline)
	if err != nil {
		errs = append(errs, err)
	}
	extraHeaders, err := lookupStringMapJSON("CONFIG_SOURCE_EXTRA_HEADERS")
	if err != nil {
		errs = append(errs, err)
	}
	macroNames, err := parseTmpxMacroNames(os.Getenv("TMPX_MACRO_NAMES"))
	if err != nil {
		errs = append(errs, err)
	}
	audienceTimeout, err := lookupDuration("AUDIENCE_TIMEOUT", defaultAudienceTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	fcapTimeout, err := lookupDuration("FCAP_TIMEOUT", defaultFCapTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	metricsEnabled, err := lookupBool("METRICS_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}
	pprofEnabled, err := lookupBool("PPROF_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}
	supportedVersions, err := lookupIntList("SUPPORTED_ADCP_MAJOR_VERSIONS", defaultSupportedADCPMajorVersions)
	if err != nil {
		errs = append(errs, err)
	}

	audienceBlock, blockErrs := loadValkeyBlock("AUDIENCE")
	errs = append(errs, blockErrs...)
	fcapBlock, blockErrs := loadValkeyBlock("FCAP")
	errs = append(errs, blockErrs...)
	// Fallback blocks are used only during a Valkey resharding — the
	// runbook sets FCAP_FALLBACK_VALKEY_SHARDS / AUDIENCE_FALLBACK_VALKEY_SHARDS
	// to the pre-migration topology, the primary block above holds the new
	// topology, and the two get OR-ed at read time. Absent either key →
	// disabled (Enabled=false), which is the default steady-state config.
	fallbackAudienceBlock, blockErrs := loadValkeyBlock("AUDIENCE_FALLBACK")
	errs = append(errs, blockErrs...)
	fallbackFcapBlock, blockErrs := loadValkeyBlock("FCAP_FALLBACK")
	errs = append(errs, blockErrs...)

	cfg := Config{
		HTTPPort:                   port,
		RequestTimeout:             reqTimeout,
		HTTPReadHeaderTimeout:      readHeaderTimeout,
		HTTPReadTimeout:            readTimeout,
		HTTPWriteTimeout:           writeTimeout,
		HTTPIdleTimeout:            idleTimeout,
		ShutdownGrace:              shutdownGrace,
		ShutdownTimeout:            shutdownTimeout,
		RequestBodyLimitBytes:      bodyLimit,
		MaxHeaderBytes:             maxHeader,
		MaxOpenConnections:         maxConns,
		ResponseTTL:                responseTTL,
		StrictContentType:          strictCT,
		AccessLogEnabled:           accessLog,
		AdminPort:                  adminPort,
		SupportedADCPMajorVersions: supportedVersions,
		LogLevel:                   lookupString("LOG_LEVEL", defaultLogLevel),
		TMP: TMPConfig{
			// TrimSpace on every field: a bearer with a trailing newline
			// becomes `Bearer <token>\n` (silent 401 at the registry);
			// a padded mode string hits the `default` branch and fails
			// startup. Both are foot-guns operators hit with env-file
			// injection tools that append newlines.
			RegistryURL:    strings.TrimSpace(os.Getenv("TMP_REGISTRY_URL")),
			RegistryMode:   strings.TrimSpace(os.Getenv("TMP_REGISTRY_MODE")),
			RegistryBearer: strings.TrimSpace(os.Getenv("TMP_REGISTRY_BEARER")),
			OwnEndpointURL: strings.TrimSpace(os.Getenv("TMP_OWN_ENDPOINT_URL")),
			AllowUnsigned:  allowUnsigned,
		},
		TMPX: TMPXConfig{
			EncryptJWKSURL: os.Getenv("TMPX_ENCRYPT_JWKS_URL"),
			EncryptJWKSTTL: jwksTTL,
			Country:        os.Getenv("TMPX_COUNTRY"),
			Priority:       os.Getenv("TMPX_PRIORITY"),
			MacroNames:     macroNames,
		},
		LiveRamp: LiveRampSidecarConfig{
			URL:         os.Getenv("LIVERAMP_SIDECAR_URL"),
			Timeout:     lrTimeout,
			DialTimeout: lrDialTimeout,
		},
		IdentityConfig: IdentityConfigSourceConfig{
			URL:                os.Getenv("CONFIG_SOURCE_URL"),
			Token:              os.Getenv("CONFIG_SOURCE_TOKEN"),
			Timeout:            cfgTimeout,
			RefreshInterval:    refreshInterval,
			StartMode:          lookupString("CONFIG_START_MODE", defaultStartMode),
			StartRetryDeadline: startRetryDeadline,
			ExtraHeaders:       extraHeaders,
		},
		AudienceValkey:         audienceBlock,
		FCapValkey:             fcapBlock,
		FallbackAudienceValkey: fallbackAudienceBlock,
		FallbackFCapValkey:     fallbackFcapBlock,
		AudienceTimeout:        audienceTimeout,
		FCapTimeout:            fcapTimeout,
		Metrics: MetricsConfig{
			Enabled:   metricsEnabled,
			Namespace: lookupString("METRICS_NAMESPACE", defaultNamespace),
		},
		Pprof: PprofConfig{
			Enabled: pprofEnabled,
		},
	}

	return cfg, errors.Join(errs...)
}

// Validate reports configuration errors. All errors are joined; callers see
// every problem at once rather than fixing them one at a time.
func (c Config) Validate() error {
	var errs []error
	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		errs = append(errs, fmt.Errorf("HTTP_PORT %d is out of range [1,65535]", c.HTTPPort))
	}
	if c.RequestTimeout <= 0 {
		errs = append(errs, errors.New("REQUEST_TIMEOUT must be positive"))
	}
	if c.ShutdownGrace < 0 {
		errs = append(errs, errors.New("SHUTDOWN_GRACE must be non-negative"))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be positive"))
	}
	if c.HTTPReadHeaderTimeout <= 0 {
		errs = append(errs, errors.New("HTTP_READ_HEADER_TIMEOUT must be positive"))
	}
	if c.HTTPReadTimeout <= 0 {
		errs = append(errs, errors.New("HTTP_READ_TIMEOUT must be positive"))
	}
	if c.HTTPReadTimeout > 0 && c.HTTPReadHeaderTimeout > c.HTTPReadTimeout {
		errs = append(errs, fmt.Errorf("HTTP_READ_HEADER_TIMEOUT (%s) must be <= HTTP_READ_TIMEOUT (%s); the inner bound is meaningless otherwise",
			c.HTTPReadHeaderTimeout, c.HTTPReadTimeout))
	}
	if c.HTTPWriteTimeout <= 0 {
		errs = append(errs, errors.New("HTTP_WRITE_TIMEOUT must be positive"))
	}
	if c.RequestTimeout > 0 && c.HTTPWriteTimeout > 0 && c.HTTPWriteTimeout <= c.RequestTimeout {
		// Go's http.Server starts the WriteTimeout clock at end-of-headers,
		// so it covers body-read + handler + response-write. If it doesn't
		// exceed the per-request internal budget, the listener kills the
		// connection before the handler finishes and the client sees a
		// truncated/closed response instead of the 200+empty fail-closed
		// shape.
		errs = append(errs, fmt.Errorf("HTTP_WRITE_TIMEOUT (%s) must be greater than REQUEST_TIMEOUT (%s); otherwise the listener cuts the response off mid-write",
			c.HTTPWriteTimeout, c.RequestTimeout))
	}
	if c.HTTPIdleTimeout <= 0 {
		errs = append(errs, errors.New("HTTP_IDLE_TIMEOUT must be positive"))
	}
	if c.RequestBodyLimitBytes <= 0 {
		errs = append(errs, errors.New("REQUEST_BODY_LIMIT_BYTES must be positive"))
	}
	if c.MaxHeaderBytes <= 0 {
		errs = append(errs, errors.New("MAX_HEADER_BYTES must be positive"))
	}
	if c.MaxOpenConnections <= 0 {
		errs = append(errs, errors.New("MAX_OPEN_CONNECTIONS must be positive"))
	}
	if c.AdminPort != 0 {
		if c.AdminPort < 1 || c.AdminPort > 65535 {
			errs = append(errs, fmt.Errorf("ADMIN_PORT %d is out of range [1,65535]", c.AdminPort))
		}
		if c.AdminPort == c.HTTPPort {
			errs = append(errs, fmt.Errorf("ADMIN_PORT (%d) must differ from HTTP_PORT", c.AdminPort))
		}
	}
	if c.ResponseTTL <= 0 {
		errs = append(errs, errors.New("RESPONSE_TTL must be positive"))
	}
	if c.ResponseTTL > time.Duration(maxServeWindowSec)*time.Second {
		errs = append(errs, fmt.Errorf("RESPONSE_TTL must be <= %ds", maxServeWindowSec))
	}
	if len(c.SupportedADCPMajorVersions) == 0 {
		errs = append(errs, errors.New("SUPPORTED_ADCP_MAJOR_VERSIONS must declare at least one major version"))
	}
	for _, v := range c.SupportedADCPMajorVersions {
		if v < 1 || v > 99 {
			errs = append(errs, fmt.Errorf("SUPPORTED_ADCP_MAJOR_VERSIONS entry %d is out of range [1,99]", v))
		}
	}
	if c.AudienceTimeout <= 0 {
		errs = append(errs, errors.New("AUDIENCE_TIMEOUT must be positive"))
	}
	if c.FCapTimeout <= 0 {
		errs = append(errs, errors.New("FCAP_TIMEOUT must be positive"))
	}
	if c.IdentityConfig.URL == "" {
		errs = append(errs, errors.New("CONFIG_SOURCE_URL is required"))
	}
	if c.IdentityConfig.Token == "" {
		errs = append(errs, errors.New("CONFIG_SOURCE_TOKEN is required"))
	}
	if c.IdentityConfig.RefreshInterval <= 0 {
		errs = append(errs, errors.New("CONFIG_REFRESH_INTERVAL must be positive"))
	}
	switch c.IdentityConfig.StartMode {
	case StartModeRetry:
		if c.IdentityConfig.StartRetryDeadline <= 0 {
			errs = append(errs, errors.New("CONFIG_START_RETRY_DEADLINE must be positive when CONFIG_START_MODE=retry"))
		}
	case StartModeFailFast, StartModeBestEffort:
		// no further validation
	default:
		errs = append(errs, fmt.Errorf("CONFIG_START_MODE %q is not one of %q, %q, %q",
			c.IdentityConfig.StartMode, StartModeRetry, StartModeFailFast, StartModeBestEffort))
	}
	if !c.TMP.AllowUnsigned {
		if c.TMP.RegistryURL == "" {
			errs = append(errs, errors.New("TMP_REGISTRY_URL is required when TMP_ALLOW_UNSIGNED=false"))
		}
		if c.TMP.OwnEndpointURL == "" {
			errs = append(errs, errors.New("TMP_OWN_ENDPOINT_URL is required when TMP_ALLOW_UNSIGNED=false"))
		}
	}
	if !c.FCapValkey.Enabled {
		errs = append(errs, errors.New("FCAP_VALKEY_* is required"))
	} else if err := c.FCapValkey.ToRedisStoreConfig().Validate(); err != nil {
		errs = append(errs, fmt.Errorf("FCAP_VALKEY: %w", err))
	}
	if c.AudienceValkey.Enabled {
		if err := c.AudienceValkey.ToRedisStoreConfig().Validate(); err != nil {
			errs = append(errs, fmt.Errorf("AUDIENCE_VALKEY: %w", err))
		}
	}
	if c.FallbackFCapValkey.Enabled {
		if !c.FCapValkey.Enabled {
			errs = append(errs, errors.New("FCAP_FALLBACK_VALKEY_* is set but FCAP_VALKEY_* is not; fallback requires a primary"))
		}
		if err := c.FallbackFCapValkey.ToRedisStoreConfig().Validate(); err != nil {
			errs = append(errs, fmt.Errorf("FCAP_FALLBACK_VALKEY: %w", err))
		}
	}
	if c.FallbackAudienceValkey.Enabled {
		if !c.AudienceValkey.Enabled {
			errs = append(errs, errors.New("AUDIENCE_FALLBACK_VALKEY_* is set but AUDIENCE_VALKEY_* is not; fallback requires a primary"))
		}
		if err := c.FallbackAudienceValkey.ToRedisStoreConfig().Validate(); err != nil {
			errs = append(errs, fmt.Errorf("AUDIENCE_FALLBACK_VALKEY: %w", err))
		}
	}
	if c.TMPX.EncryptJWKSURL != "" || c.TMPX.Country != "" || c.TMPX.Priority != "" {
		if c.TMPX.EncryptJWKSURL == "" {
			errs = append(errs, errors.New("TMPX_ENCRYPT_JWKS_URL is required when any TMPX_* is set"))
		}
		if c.TMPX.Country == "" {
			errs = append(errs, errors.New("TMPX_COUNTRY is required when any TMPX_* is set"))
		}
	}
	if c.LiveRamp.URL != "" {
		if !strings.HasPrefix(c.LiveRamp.URL, "http://") && !strings.HasPrefix(c.LiveRamp.URL, "https://") {
			errs = append(errs, fmt.Errorf("LIVERAMP_SIDECAR_URL %q must use http:// or https://", c.LiveRamp.URL))
		}
		if c.LiveRamp.Timeout < 0 {
			errs = append(errs, errors.New("LIVERAMP_SIDECAR_TIMEOUT must be non-negative"))
		}
		if c.LiveRamp.DialTimeout < 0 {
			errs = append(errs, errors.New("LIVERAMP_SIDECAR_DIAL_TIMEOUT must be non-negative"))
		}
	}
	if c.Metrics.Enabled {
		if c.Metrics.Namespace == "" {
			errs = append(errs, errors.New("METRICS_NAMESPACE must be non-empty when METRICS_ENABLED=true"))
		} else if !isValidPromName(c.Metrics.Namespace) {
			errs = append(errs, fmt.Errorf("METRICS_NAMESPACE %q is not a valid Prometheus name (must match [a-zA-Z_][a-zA-Z0-9_]*)", c.Metrics.Namespace))
		}
	}
	return errors.Join(errs...)
}

// loadValkeyBlock reads {prefix}_VALKEY_* env vars into a ValkeyBlock. The
// block is marked Enabled when SHARDS parses to a non-empty JSON object.
// An unset SHARDS env var, an empty string, and a valid-JSON empty object
// ("{}") all collapse to the same disabled block — a ConfigMap emitter
// can't easily omit a key, so an unconfigured block often lands as "{}"
// on the pod and len(shards)==0 is never a useful runtime configuration.
//
// For *_FALLBACK prefixes, an empty SHARDS combined with a non-empty
// sibling {prefix}_VALKEY_MODE is "intended but broken" — the operator
// meant to wire the fallback but the SHARDS render came out empty.
// Loud error instead of silent disable so a mis-emitted config during
// a resharding window doesn't quietly turn the fallback off (which
// would drop the union OR and reopen the exact stale-membership window
// the fallback exists to close). Primary prefixes are exempt: the
// AUDIENCE primary is optional, and a deployment running with audience
// intentionally disabled can still have the chart emit a default MODE.
func loadValkeyBlock(prefix string) (ValkeyBlock, []error) {
	var errs []error
	shardsRaw := os.Getenv(prefix + "_VALKEY_SHARDS")
	shardsEmpty := shardsRaw == ""

	shards := map[string]string{}
	if !shardsEmpty {
		if err := json.Unmarshal([]byte(shardsRaw), &shards); err != nil {
			return ValkeyBlock{}, []error{fmt.Errorf("%s_VALKEY_SHARDS is not a JSON object: %w", prefix, err)}
		}
	}
	if len(shards) == 0 {
		if strings.HasSuffix(prefix, "_FALLBACK") {
			if mode, ok := os.LookupEnv(prefix + "_VALKEY_MODE"); ok && mode != "" {
				return ValkeyBlock{}, []error{fmt.Errorf("%s_VALKEY_MODE is set but %s_VALKEY_SHARDS is empty or {}; either configure both or unset both", prefix, prefix)}
			}
		}
		return ValkeyBlock{}, nil
	}

	mode := lookupString(prefix+"_VALKEY_MODE", string(redisstore.ModeStandalone))
	db, err := lookupInt(prefix+"_VALKEY_DB", 0)
	if err != nil {
		errs = append(errs, err)
	}
	tls, err := lookupBool(prefix+"_VALKEY_TLS", false)
	if err != nil {
		errs = append(errs, err)
	}
	dial, err := lookupDuration(prefix+"_VALKEY_DIAL_TIMEOUT", 5*time.Second)
	if err != nil {
		errs = append(errs, err)
	}
	read, err := lookupDuration(prefix+"_VALKEY_READ_TIMEOUT", 0)
	if err != nil {
		errs = append(errs, err)
	}
	pool, err := lookupInt(prefix+"_VALKEY_POOL_SIZE", 0)
	if err != nil {
		errs = append(errs, err)
	}

	return ValkeyBlock{
		Enabled:     true,
		Mode:        mode,
		Shards:      shards,
		Username:    os.Getenv(prefix + "_VALKEY_USERNAME"),
		Password:    os.Getenv(prefix + "_VALKEY_PASSWORD"),
		DB:          db,
		TLS:         tls,
		DialTimeout: dial,
		ReadTimeout: read,
		PoolSize:    pool,
	}, errs
}

func lookupString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// parseTmpxMacroNames splits TMPX_MACRO_NAMES (comma-separated, e.g.
// `S3_TMPX` or `S3_TMPX_1,S3_TMPX_2`) into the ordered slot list emitted on
// IdentityMatchResponse.tmpx_macros[]. Empty / whitespace-only values yield
// nil, which keeps the legacy single-`tmpx`-string emission shape — no new
// behavior until the env var is set.
//
// The v1 spec caps the registered list at tmproto.TmpxMaxSlots
// (provider-registration.json `tmpx_macros.maxItems`) because each slot carries
// at most TmpxMaxWireBytes of the sealed wire and the receiver's OpenTmpx
// bound is TmpxMaxSlots * TmpxMaxWireBytes. An unbounded name list would
// silently produce a wire the sealer's own conformant receiver refuses;
// reject it at startup with a clear message rather than corrupt tokens at
// serving time.
func parseTmpxMacroNames(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		names = append(names, p)
	}
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > tmproto.TmpxMaxSlots {
		return nil, fmt.Errorf("TMPX_MACRO_NAMES has %d entries, exceeds the v1 cap of %d (provider-registration.json `tmpx_macros.maxItems`); each slot carries at most %d bytes of the sealed wire and the receiver's OpenTmpx bound is %d * %d bytes",
			len(names), tmproto.TmpxMaxSlots, tmproto.TmpxMaxWireBytes, tmproto.TmpxMaxSlots, tmproto.TmpxMaxWireBytes)
	}
	return names, nil
}

func lookupInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not an integer: %w", name, v, err)
	}
	return n, nil
}

func lookupBool(name string, def bool) (bool, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean: %w", name, v, err)
	}
	return b, nil
}

func lookupDuration(name string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration: %w", name, v, err)
	}
	return d, nil
}

// lookupIntList parses a comma-separated list of integers from env. Returns
// a copy of def when the variable is unset; the copy isolates callers from
// later mutation of the default slice. Empty entries are rejected so a
// trailing comma surfaces as a config error rather than a silent typo.
func lookupIntList(name string, def []int) ([]int, error) {
	v := os.Getenv(name)
	if v == "" {
		out := make([]int, len(def))
		copy(out, def)
		return out, nil
	}
	parts := strings.Split(v, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("%s=%q contains an empty entry", name, v)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%s=%q has non-integer entry %q: %w", name, v, p, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// lookupStringMapJSON parses an env var as a JSON object of string→string.
// Returns nil (with nil error) when the variable is unset or empty. Rejects
// any non-object payload and empty keys so configuration errors surface at
// startup rather than at first refresh.
func lookupStringMapJSON(name string) (map[string]string, error) {
	v := os.Getenv(name)
	if v == "" {
		return nil, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object of string→string: %w", name, err)
	}
	for k := range out {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%s contains an empty key", name)
		}
	}
	return out, nil
}

// isValidPromName mirrors the Prometheus metric name grammar
// ([a-zA-Z_][a-zA-Z0-9_]*) used for namespace prefixes.
func isValidPromName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
