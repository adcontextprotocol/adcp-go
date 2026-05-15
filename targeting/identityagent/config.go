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
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
)

// Config is the env-derived configuration for a single identity-agent
// process. Build with LoadConfigFromEnv and inspect with Validate before
// passing to Run.
type Config struct {
	HTTPPort       int
	RequestTimeout time.Duration
	ShutdownGrace  time.Duration
	LogLevel       string

	TMP             TMPConfig
	TMPX            TMPXConfig
	IdentityConfig  IdentityConfigSourceConfig
	AudienceValkey  ValkeyBlock
	FCapValkey      ValkeyBlock
	AudienceTimeout time.Duration
	FCapTimeout     time.Duration

	Metrics MetricsConfig
	Pprof   PprofConfig
}

// TMPConfig drives TMP signature verification on /tmp/identity.
type TMPConfig struct {
	RegistryURL     string
	OwnEndpointURL  string
	AllowUnsigned   bool
}

// TMPXConfig drives TMPX response sealing. Disabled when EncryptJWKSURL is
// empty.
type TMPXConfig struct {
	EncryptJWKSURL  string
	EncryptJWKSTTL  time.Duration
	Country         string
	Priority        string
	ReferenceStubAck bool
}

// IdentityConfigSourceConfig drives the Scope3 identity-config refresh
// service.
//
// StartMode controls behavior when the initial LoadAll fails:
//   - "retry"       (default) — block startup retrying until StartRetryDeadline
//   - "fail-fast"             — exit on the first failure
//   - "best-effort"           — start with an empty snapshot; rely on the
//                               normal refresh tick to populate
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
}

// ValkeyBlock is the per-backend Valkey configuration. Use ToRedisStoreConfig
// to project onto the redisstore.Config the Build helper consumes.
type ValkeyBlock struct {
	Enabled bool

	Mode     string
	Shards   map[string]string
	Username string
	Password string
	DB       int
	TLS      bool

	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PoolSize     int
}

// ToRedisStoreConfig projects onto the redisstore.Config the Build helper
// consumes. Only meaningful when Enabled is true; the caller is responsible
// for gating the call (the identityagent.Config.Validate flow does this).
func (b ValkeyBlock) ToRedisStoreConfig() redisstore.Config {
	return redisstore.Config{
		Mode:         redisstore.Mode(b.Mode),
		Shards:       b.Shards,
		Username:     b.Username,
		Password:     b.Password,
		DB:           b.DB,
		TLS:          b.TLS,
		DialTimeout:  b.DialTimeout,
		ReadTimeout:  b.ReadTimeout,
		WriteTimeout: b.WriteTimeout,
		PoolSize:     b.PoolSize,
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
	defaultHTTPPort           = 8080
	defaultRequestTimeout     = 40 * time.Millisecond
	defaultShutdownGrace      = 1 * time.Second
	defaultLogLevel           = "info"
	defaultJWKSTTL            = 5 * time.Minute
	defaultConfigTimeout      = 30 * time.Second
	defaultRefreshInterval    = 5 * time.Minute
	defaultStartMode          = "retry"
	defaultStartRetryDeadline = 5 * time.Minute
	defaultAudienceTimeout    = 10 * time.Millisecond
	defaultFCapTimeout        = 10 * time.Millisecond
	defaultNamespace          = "identity_agent"
)

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
	allowUnsigned, err := lookupBool("TMP_ALLOW_UNSIGNED", false)
	if err != nil {
		errs = append(errs, err)
	}
	jwksTTL, err := lookupDuration("TMPX_ENCRYPT_JWKS_TTL", defaultJWKSTTL)
	if err != nil {
		errs = append(errs, err)
	}
	stubAck, err := lookupBool("TMPX_REFERENCE_STUB_ACK", false)
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

	audienceBlock, blockErrs := loadValkeyBlock("AUDIENCE")
	errs = append(errs, blockErrs...)
	fcapBlock, blockErrs := loadValkeyBlock("FCAP")
	errs = append(errs, blockErrs...)

	cfg := Config{
		HTTPPort:       port,
		RequestTimeout: reqTimeout,
		ShutdownGrace:  shutdownGrace,
		LogLevel:       lookupString("LOG_LEVEL", defaultLogLevel),
		TMP: TMPConfig{
			RegistryURL:    os.Getenv("TMP_REGISTRY_URL"),
			OwnEndpointURL: os.Getenv("TMP_OWN_ENDPOINT_URL"),
			AllowUnsigned:  allowUnsigned,
		},
		TMPX: TMPXConfig{
			EncryptJWKSURL:   os.Getenv("TMPX_ENCRYPT_JWKS_URL"),
			EncryptJWKSTTL:   jwksTTL,
			Country:          os.Getenv("TMPX_COUNTRY"),
			Priority:         os.Getenv("TMPX_PRIORITY"),
			ReferenceStubAck: stubAck,
		},
		IdentityConfig: IdentityConfigSourceConfig{
			URL:                os.Getenv("CONFIG_SOURCE_URL"),
			Token:              os.Getenv("CONFIG_SOURCE_TOKEN"),
			Timeout:            cfgTimeout,
			RefreshInterval:    refreshInterval,
			StartMode:          lookupString("CONFIG_START_MODE", defaultStartMode),
			StartRetryDeadline: startRetryDeadline,
		},
		AudienceValkey:  audienceBlock,
		FCapValkey:      fcapBlock,
		AudienceTimeout: audienceTimeout,
		FCapTimeout:     fcapTimeout,
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
	if c.TMPX.EncryptJWKSURL != "" || c.TMPX.Country != "" || c.TMPX.Priority != "" {
		if c.TMPX.EncryptJWKSURL == "" {
			errs = append(errs, errors.New("TMPX_ENCRYPT_JWKS_URL is required when any TMPX_* is set"))
		}
		if c.TMPX.Country == "" {
			errs = append(errs, errors.New("TMPX_COUNTRY is required when any TMPX_* is set"))
		}
		if c.TMPX.EncryptJWKSURL != "" && !c.TMPX.ReferenceStubAck {
			errs = append(errs, errors.New("TMPX_REFERENCE_STUB_ACK=true is required to enable TMPX with the reference SHA-512 stub encoder"))
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
// block is marked Enabled when the SHARDS variable is present and non-empty
// — that single variable acts as the "configured" signal.
func loadValkeyBlock(prefix string) (ValkeyBlock, []error) {
	var errs []error
	shardsRaw := os.Getenv(prefix + "_VALKEY_SHARDS")
	if shardsRaw == "" {
		return ValkeyBlock{}, nil
	}

	shards := map[string]string{}
	if err := json.Unmarshal([]byte(shardsRaw), &shards); err != nil {
		return ValkeyBlock{}, []error{fmt.Errorf("%s_VALKEY_SHARDS is not a JSON object: %w", prefix, err)}
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
	write, err := lookupDuration(prefix+"_VALKEY_WRITE_TIMEOUT", 0)
	if err != nil {
		errs = append(errs, err)
	}
	pool, err := lookupInt(prefix+"_VALKEY_POOL_SIZE", 0)
	if err != nil {
		errs = append(errs, err)
	}

	return ValkeyBlock{
		Enabled:      true,
		Mode:         mode,
		Shards:       shards,
		Username:     os.Getenv(prefix + "_VALKEY_USERNAME"),
		Password:     os.Getenv(prefix + "_VALKEY_PASSWORD"),
		DB:           db,
		TLS:          tls,
		DialTimeout:  dial,
		ReadTimeout:  read,
		WriteTimeout: write,
		PoolSize:     pool,
	}, errs
}

func lookupString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
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

