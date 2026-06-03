// Package contextagent assembles a production-ready TMP context-match
// service. It composes the targeting.ContextEngine with a Valkey-backed
// storage layer (per-domain reader services from mediabuystore,
// pkgconfigstore, urlliststore, suppressionstore, topicstore),
// optionally fronted by LRU caches; surfaces them behind TMP-signature
// verification on /context; exports Prometheus-compatible metrics; and
// orchestrates a coordinated graceful shutdown.
package contextagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
)

// Config is the env-derived configuration for a single context-agent
// process. Build with LoadConfigFromEnv and inspect with Validate
// before passing to Run.
type Config struct {
	HTTPPort       int
	RequestTimeout time.Duration

	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration

	ShutdownGrace   time.Duration
	ShutdownTimeout time.Duration

	RequestBodyLimitBytes int
	MaxHeaderBytes        int
	MaxOpenConnections    int

	ResponseTTL time.Duration

	StrictContentType bool
	AccessLogEnabled  bool

	AdminPort int

	SupportedADCPMajorVersions []int

	LogLevel string

	// ProviderID is stamped into suppression keys (see
	// suppressionstore) and emitted on logs / metrics.
	ProviderID string

	// SellerAgentURL is the canonicalized seller_agent_url this
	// deployment represents. Same byte-for-byte string match as
	// identityconfig.
	SellerAgentURL string

	// AcceptedTaxonomies enumerates the topic taxonomies the engine
	// trusts on inbound ContextSignals and consults on Valkey lookups.
	// Required: an empty list fails-closed on every TopicTargets
	// package.
	AcceptedTaxonomies []topicstore.Taxonomy

	// PropertyRIDs is the global property bitmap the engine checks at
	// the top of every request. A request whose property_rid is not in
	// this list short-circuits before any storage lookup.
	//
	// TODO(context-agent-followup): replace with a registry.Syncer
	// hookup that hydrates the bitmap from registry/persist Store
	// (PR #358). This static env is a stopgap that lets the agent run
	// end-to-end without the registry feed wired in.
	PropertyRIDs []string

	// SuppressionRefreshInterval is how often the agent re-scans
	// suppress:{provider_id}:* into its in-memory snapshot. External
	// writes (operator action, future writer pipeline) become visible
	// within this interval.
	SuppressionRefreshInterval time.Duration

	TMP    TMPConfig
	Valkey ValkeyBlock
	Cache  CacheConfig

	Metrics MetricsConfig
	Pprof   PprofConfig
}

// TMPConfig drives TMP signature verification on /context.
type TMPConfig struct {
	RegistryURL    string
	OwnEndpointURL string
	AllowUnsigned  bool
}

// ValkeyBlock is the Valkey configuration. The context-agent issues
// reads against media-buy, package-config, URL-list, topic, and
// suppression keys, plus writes for operator-driven suppressions (via
// the suppressionstore.Service).
type ValkeyBlock struct {
	Enabled bool

	// ShardsSupplied is true when VALKEY_SHARDS was set on the
	// environment, even if it failed to parse. Lets Validate
	// distinguish "operator forgot to set the shards" from "operator
	// set them but the JSON is malformed" so it doesn't pile a
	// generic "required" error on top of the parser's specific one.
	// Hand-constructed Config{} for tests should set this when they
	// want Validate to think the env was supplied.
	ShardsSupplied bool

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

// ToRedisStoreConfig projects onto the redisstore.Config the Build
// helper consumes.
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

// CacheConfig groups the per-domain LRU knobs. Each domain has its own
// size / TTL plus an enable flag; the master Enabled toggle bypasses
// every cache layer regardless of per-domain settings.
type CacheConfig struct {
	// Enabled is the master switch. When false, every reader is
	// constructed as a direct (uncached) reader regardless of
	// per-domain settings. Default true.
	Enabled bool

	MediaBuy  MediaBuyCacheConfig
	PkgConfig DomainCacheConfig
	URLList   DomainCacheConfig
	Topics    TopicsCacheConfig
}

// DomainCacheConfig is the common shape for domains with a single LRU.
type DomainCacheConfig struct {
	Enabled bool
	Size    int
	TTL     time.Duration
}

// MediaBuyCacheConfig has two underlying caches: the seller-set cache
// and the per-buy record cache. They can be sized independently.
type MediaBuyCacheConfig struct {
	Enabled       bool
	SellerSetSize int
	SellerSetTTL  time.Duration
	MediaBuySize  int
	MediaBuyTTL   time.Duration
}

// TopicsCacheConfig has two underlying caches: artifact-side and
// package-side. They can be sized independently.
type TopicsCacheConfig struct {
	Enabled      bool
	ArtifactSize int
	ArtifactTTL  time.Duration
	PackageSize  int
	PackageTTL   time.Duration
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
	defaultHTTPPort              = 8081
	defaultRequestTimeout        = 40 * time.Millisecond
	defaultHTTPReadHeaderTimeout = 200 * time.Millisecond
	defaultHTTPReadTimeout       = 500 * time.Millisecond
	defaultHTTPWriteTimeout      = 1 * time.Second
	defaultHTTPIdleTimeout       = 30 * time.Second
	defaultShutdownGrace         = 1 * time.Second
	defaultShutdownTimeout       = 10 * time.Second
	defaultRequestBodyLimitBytes = 256 * 1024 // larger than identity: artifact payloads
	defaultMaxHeaderBytes        = 8 * 1024
	defaultMaxOpenConnections    = 1024
	defaultResponseTTL           = 60 * time.Second
	defaultStrictContentType     = true
	defaultAccessLogEnabled      = false
	defaultAdminPort             = 0
	defaultLogLevel              = "info"
	defaultSuppressionRefresh    = 5 * time.Minute
	defaultNamespace             = "context_agent"

	defaultMediaBuySellerSize = 1024
	defaultMediaBuySellerTTL  = 5 * time.Minute
	defaultMediaBuyRecSize    = 4096
	defaultMediaBuyRecTTL     = 5 * time.Minute

	defaultPkgConfigSize = 4096
	defaultPkgConfigTTL  = 5 * time.Minute

	defaultURLListSize = 16384
	defaultURLListTTL  = 1 * time.Minute

	defaultTopicArtifactSize = 65536
	defaultTopicArtifactTTL  = 5 * time.Minute
	defaultTopicPackageSize  = 4096
	defaultTopicPackageTTL   = 5 * time.Minute
)

var defaultSupportedADCPMajorVersions = []int{3}

// LoadConfigFromEnv reads every recognized environment variable into a
// Config. Validation is the caller's responsibility — call Validate
// before using the result.
func LoadConfigFromEnv() (Config, error) {
	var errs []error

	port, err := lookupInt("HTTP_PORT", defaultHTTPPort)
	errs = appendErr(errs, err)
	reqTimeout, err := lookupDuration("REQUEST_TIMEOUT", defaultRequestTimeout)
	errs = appendErr(errs, err)
	shutdownGrace, err := lookupDuration("SHUTDOWN_GRACE", defaultShutdownGrace)
	errs = appendErr(errs, err)
	shutdownTimeout, err := lookupDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	errs = appendErr(errs, err)
	readHeaderTimeout, err := lookupDuration("HTTP_READ_HEADER_TIMEOUT", defaultHTTPReadHeaderTimeout)
	errs = appendErr(errs, err)
	readTimeout, err := lookupDuration("HTTP_READ_TIMEOUT", defaultHTTPReadTimeout)
	errs = appendErr(errs, err)
	writeTimeout, err := lookupDuration("HTTP_WRITE_TIMEOUT", defaultHTTPWriteTimeout)
	errs = appendErr(errs, err)
	idleTimeout, err := lookupDuration("HTTP_IDLE_TIMEOUT", defaultHTTPIdleTimeout)
	errs = appendErr(errs, err)
	bodyLimit, err := lookupInt("REQUEST_BODY_LIMIT_BYTES", defaultRequestBodyLimitBytes)
	errs = appendErr(errs, err)
	maxHeader, err := lookupInt("MAX_HEADER_BYTES", defaultMaxHeaderBytes)
	errs = appendErr(errs, err)
	maxConns, err := lookupInt("MAX_OPEN_CONNECTIONS", defaultMaxOpenConnections)
	errs = appendErr(errs, err)
	strictCT, err := lookupBool("STRICT_CONTENT_TYPE", defaultStrictContentType)
	errs = appendErr(errs, err)
	accessLog, err := lookupBool("ACCESS_LOG_ENABLED", defaultAccessLogEnabled)
	errs = appendErr(errs, err)
	adminPort, err := lookupInt("ADMIN_PORT", defaultAdminPort)
	errs = appendErr(errs, err)
	responseTTL, err := lookupDuration("RESPONSE_TTL", defaultResponseTTL)
	errs = appendErr(errs, err)
	allowUnsigned, err := lookupBool("TMP_ALLOW_UNSIGNED", false)
	errs = appendErr(errs, err)
	suppressionRefresh, err := lookupDuration("SUPPRESSION_REFRESH_INTERVAL", defaultSuppressionRefresh)
	errs = appendErr(errs, err)

	supportedVers, err := lookupIntList("SUPPORTED_ADCP_MAJOR_VERSIONS", defaultSupportedADCPMajorVersions)
	errs = appendErr(errs, err)

	taxonomies, err := lookupTaxonomies("ACCEPTED_TAXONOMIES")
	errs = appendErr(errs, err)

	propertyRIDs := lookupStringList("PROPERTY_RIDS")

	cacheCfg, err := loadCacheConfigFromEnv()
	errs = appendErr(errs, err)

	valkeyCfg, err := loadValkeyBlockFromEnv()
	errs = appendErr(errs, err)

	metricsEnabled, err := lookupBool("METRICS_ENABLED", false)
	errs = appendErr(errs, err)
	pprofEnabled, err := lookupBool("PPROF_ENABLED", false)
	errs = appendErr(errs, err)

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
		SupportedADCPMajorVersions: supportedVers,
		LogLevel:                   strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		ProviderID:                 strings.TrimSpace(os.Getenv("PROVIDER_ID")),
		SellerAgentURL:             strings.TrimSpace(os.Getenv("SELLER_AGENT_URL")),
		AcceptedTaxonomies:         taxonomies,
		PropertyRIDs:               propertyRIDs,
		SuppressionRefreshInterval: suppressionRefresh,
		TMP: TMPConfig{
			RegistryURL:    strings.TrimSpace(os.Getenv("TMP_REGISTRY_URL")),
			OwnEndpointURL: strings.TrimSpace(os.Getenv("TMP_OWN_ENDPOINT_URL")),
			AllowUnsigned:  allowUnsigned,
		},
		Valkey: valkeyCfg,
		Cache:  cacheCfg,
		Metrics: MetricsConfig{
			Enabled:   metricsEnabled,
			Namespace: stringOr(os.Getenv("METRICS_NAMESPACE"), defaultNamespace),
		},
		Pprof: PprofConfig{Enabled: pprofEnabled},
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = defaultLogLevel
	}
	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}

// Validate runs cross-field invariants on a loaded Config.
func (c Config) Validate() error {
	var errs []error
	if c.HTTPPort <= 0 || c.HTTPPort > 65535 {
		errs = append(errs, fmt.Errorf("HTTP_PORT %d out of range", c.HTTPPort))
	}
	if c.RequestTimeout <= 0 {
		errs = append(errs, errors.New("REQUEST_TIMEOUT must be positive"))
	}
	if c.HTTPWriteTimeout < c.RequestTimeout {
		errs = append(errs, errors.New("HTTP_WRITE_TIMEOUT must be >= REQUEST_TIMEOUT"))
	}
	if c.HTTPReadTimeout < c.HTTPReadHeaderTimeout {
		errs = append(errs, errors.New("HTTP_READ_TIMEOUT must be >= HTTP_READ_HEADER_TIMEOUT"))
	}
	if c.RequestBodyLimitBytes <= 0 {
		errs = append(errs, errors.New("REQUEST_BODY_LIMIT_BYTES must be positive"))
	}
	if c.MaxOpenConnections <= 0 {
		errs = append(errs, errors.New("MAX_OPEN_CONNECTIONS must be positive"))
	}
	if c.ProviderID == "" {
		errs = append(errs, errors.New("PROVIDER_ID is required"))
	}
	if c.SellerAgentURL == "" {
		errs = append(errs, errors.New("SELLER_AGENT_URL is required"))
	}
	if !c.TMP.AllowUnsigned {
		if c.TMP.RegistryURL == "" {
			errs = append(errs, errors.New("TMP_REGISTRY_URL is required unless TMP_ALLOW_UNSIGNED=true"))
		}
		if c.TMP.OwnEndpointURL == "" {
			errs = append(errs, errors.New("TMP_OWN_ENDPOINT_URL is required unless TMP_ALLOW_UNSIGNED=true"))
		}
	}
	// Only emit "VALKEY_SHARDS is required" when no value at all was
	// supplied; if the env was set but malformed, the parser already
	// surfaced a more specific error and stacking "required" on top
	// would mislead the operator.
	if !c.Valkey.Enabled && !c.Valkey.ShardsSupplied {
		errs = append(errs, errors.New("VALKEY_SHARDS is required"))
	}
	if c.AdminPort == 0 && c.Pprof.Enabled {
		errs = append(errs, errors.New("PPROF_ENABLED=true requires ADMIN_PORT > 0 — refusing to mount pprof on the public /context listener"))
	}
	if c.SuppressionRefreshInterval <= 0 {
		errs = append(errs, errors.New("SUPPRESSION_REFRESH_INTERVAL must be positive"))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be positive"))
	}
	if len(c.SupportedADCPMajorVersions) == 0 {
		errs = append(errs, errors.New("SUPPORTED_ADCP_MAJOR_VERSIONS must list at least one version"))
	}
	if err := validateCacheSizes(c.Cache); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// validateCacheSizes rejects zero / negative sizes on any cache marked
// Enabled. The hashicorp expirable LRU constructor accepts size == 0
// but produces a no-op cache, which makes the operator-visible
// CACHE_*_ENABLED toggle silently misleading.
func validateCacheSizes(c CacheConfig) error {
	var errs []error
	if c.Enabled && c.MediaBuy.Enabled {
		if c.MediaBuy.SellerSetSize <= 0 {
			errs = append(errs, errors.New("CACHE_MEDIABUY_SELLER_SIZE must be positive when CACHE_MEDIABUY_ENABLED=true"))
		}
		if c.MediaBuy.MediaBuySize <= 0 {
			errs = append(errs, errors.New("CACHE_MEDIABUY_RECORD_SIZE must be positive when CACHE_MEDIABUY_ENABLED=true"))
		}
	}
	if c.Enabled && c.PkgConfig.Enabled && c.PkgConfig.Size <= 0 {
		errs = append(errs, errors.New("CACHE_PKGCONFIG_SIZE must be positive when CACHE_PKGCONFIG_ENABLED=true"))
	}
	if c.Enabled && c.URLList.Enabled && c.URLList.Size <= 0 {
		errs = append(errs, errors.New("CACHE_URLLIST_SIZE must be positive when CACHE_URLLIST_ENABLED=true"))
	}
	if c.Enabled && c.Topics.Enabled {
		if c.Topics.ArtifactSize <= 0 {
			errs = append(errs, errors.New("CACHE_TOPICS_ARTIFACT_SIZE must be positive when CACHE_TOPICS_ENABLED=true"))
		}
		if c.Topics.PackageSize <= 0 {
			errs = append(errs, errors.New("CACHE_TOPICS_PACKAGE_SIZE must be positive when CACHE_TOPICS_ENABLED=true"))
		}
	}
	return errors.Join(errs...)
}

func loadCacheConfigFromEnv() (CacheConfig, error) {
	var errs []error
	master, err := lookupBool("CACHE_ENABLED", true)
	errs = appendErr(errs, err)

	mbSellerEnabled, err := lookupBool("CACHE_MEDIABUY_ENABLED", true)
	errs = appendErr(errs, err)
	mbSellerSize, err := lookupInt("CACHE_MEDIABUY_SELLER_SIZE", defaultMediaBuySellerSize)
	errs = appendErr(errs, err)
	mbSellerTTL, err := lookupDuration("CACHE_MEDIABUY_SELLER_TTL", defaultMediaBuySellerTTL)
	errs = appendErr(errs, err)
	mbRecSize, err := lookupInt("CACHE_MEDIABUY_RECORD_SIZE", defaultMediaBuyRecSize)
	errs = appendErr(errs, err)
	mbRecTTL, err := lookupDuration("CACHE_MEDIABUY_RECORD_TTL", defaultMediaBuyRecTTL)
	errs = appendErr(errs, err)

	pcEnabled, err := lookupBool("CACHE_PKGCONFIG_ENABLED", true)
	errs = appendErr(errs, err)
	pcSize, err := lookupInt("CACHE_PKGCONFIG_SIZE", defaultPkgConfigSize)
	errs = appendErr(errs, err)
	pcTTL, err := lookupDuration("CACHE_PKGCONFIG_TTL", defaultPkgConfigTTL)
	errs = appendErr(errs, err)

	ulEnabled, err := lookupBool("CACHE_URLLIST_ENABLED", true)
	errs = appendErr(errs, err)
	ulSize, err := lookupInt("CACHE_URLLIST_SIZE", defaultURLListSize)
	errs = appendErr(errs, err)
	ulTTL, err := lookupDuration("CACHE_URLLIST_TTL", defaultURLListTTL)
	errs = appendErr(errs, err)

	tEnabled, err := lookupBool("CACHE_TOPICS_ENABLED", true)
	errs = appendErr(errs, err)
	tArtSize, err := lookupInt("CACHE_TOPICS_ARTIFACT_SIZE", defaultTopicArtifactSize)
	errs = appendErr(errs, err)
	tArtTTL, err := lookupDuration("CACHE_TOPICS_ARTIFACT_TTL", defaultTopicArtifactTTL)
	errs = appendErr(errs, err)
	tPkgSize, err := lookupInt("CACHE_TOPICS_PACKAGE_SIZE", defaultTopicPackageSize)
	errs = appendErr(errs, err)
	tPkgTTL, err := lookupDuration("CACHE_TOPICS_PACKAGE_TTL", defaultTopicPackageTTL)
	errs = appendErr(errs, err)

	return CacheConfig{
		Enabled: master,
		MediaBuy: MediaBuyCacheConfig{
			Enabled:       mbSellerEnabled,
			SellerSetSize: mbSellerSize, SellerSetTTL: mbSellerTTL,
			MediaBuySize: mbRecSize, MediaBuyTTL: mbRecTTL,
		},
		PkgConfig: DomainCacheConfig{Enabled: pcEnabled, Size: pcSize, TTL: pcTTL},
		URLList:   DomainCacheConfig{Enabled: ulEnabled, Size: ulSize, TTL: ulTTL},
		Topics: TopicsCacheConfig{
			Enabled:      tEnabled,
			ArtifactSize: tArtSize, ArtifactTTL: tArtTTL,
			PackageSize: tPkgSize, PackageTTL: tPkgTTL,
		},
	}, errors.Join(errs...)
}

func loadValkeyBlockFromEnv() (ValkeyBlock, error) {
	var errs []error
	mode := strings.TrimSpace(os.Getenv("VALKEY_MODE"))
	if mode == "" {
		mode = "standalone"
	}
	shards, err := parseShardsJSON(os.Getenv("VALKEY_SHARDS"))
	errs = appendErr(errs, err)
	db, err := lookupInt("VALKEY_DB", 0)
	errs = appendErr(errs, err)
	tlsOn, err := lookupBool("VALKEY_TLS", false)
	errs = appendErr(errs, err)
	dial, err := lookupDuration("VALKEY_DIAL_TIMEOUT", 5*time.Second)
	errs = appendErr(errs, err)
	rt, err := lookupDuration("VALKEY_READ_TIMEOUT", 20*time.Millisecond)
	errs = appendErr(errs, err)
	wt, err := lookupDuration("VALKEY_WRITE_TIMEOUT", 50*time.Millisecond)
	errs = appendErr(errs, err)
	pool, err := lookupInt("VALKEY_POOL_SIZE", 0)
	errs = appendErr(errs, err)

	return ValkeyBlock{
		Enabled:        len(shards) > 0,
		ShardsSupplied: os.Getenv("VALKEY_SHARDS") != "",
		Mode:           mode,
		Shards:         shards,
		Username:       strings.TrimSpace(os.Getenv("VALKEY_USERNAME")),
		Password:       os.Getenv("VALKEY_PASSWORD"),
		DB:             db,
		TLS:            tlsOn,
		DialTimeout:    dial,
		ReadTimeout:    rt,
		WriteTimeout:   wt,
		PoolSize:       pool,
	}, errors.Join(errs...)
}

// --- helpers ---

func lookupInt(name string, def int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func lookupBool(name string, def bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func lookupDuration(name string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return def, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func lookupStringList(name string) []string {
	raw := os.Getenv(name)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func lookupIntList(name string, def []int) ([]int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return def, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return def, nil
	}
	return out, nil
}

// lookupTaxonomies parses ACCEPTED_TAXONOMIES as a comma-separated list
// of "source:id" pairs. Examples:
//
//	ACCEPTED_TAXONOMIES=iab:7
//	ACCEPTED_TAXONOMIES=iab:7,custom_v1:1
//
// Empty environment variable yields nil; Validate then rejects empty
// when topic targeting is in use on any package.
func lookupTaxonomies(name string) ([]topicstore.Taxonomy, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]topicstore.Taxonomy, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		colon := strings.LastIndex(p, ":")
		if colon < 1 || colon == len(p)-1 {
			return nil, fmt.Errorf("%s: %q is not in source:id form", name, p)
		}
		id, err := strconv.Atoi(p[colon+1:])
		if err != nil {
			return nil, fmt.Errorf("%s: %q has non-integer id: %w", name, p, err)
		}
		tax := topicstore.Taxonomy{Source: p[:colon], ID: id}
		if err := tax.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %q: %w", name, p, err)
		}
		out = append(out, tax)
	}
	return out, nil
}

func parseShardsJSON(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("VALKEY_SHARDS: %w", err)
	}
	return m, nil
}

func appendErr(errs []error, err error) []error {
	if err != nil {
		errs = append(errs, err)
	}
	return errs
}

func stringOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
