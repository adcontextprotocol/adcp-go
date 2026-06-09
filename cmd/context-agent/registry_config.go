package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// registryConfig configures the optional registry sync loop that feeds
// the context-agent's global property bitmap. When Enabled is false the
// agent falls back to the static PROPERTY_RIDS env var inside the
// contextagent package.
type registryConfig struct {
	Enabled bool

	FeedURL   string
	FeedToken string

	PollInterval   time.Duration
	BootstrapLimit int
	FeedLimit      int

	// CursorPath, when non-empty, persists the feed cursor to disk so
	// restarts resume against it instead of bootstrapping. Required in
	// production; tests may leave it empty to keep the syncer in
	// memory-cursor mode.
	CursorPath string

	// Backend selects how registry indexes are persisted. When set to
	// "redis" the registry/redisstore package is wired against
	// registry-specific Redis/Valkey endpoint config (RegistryRedisAddr
	// + RegistryRedisPassword) so it stays decoupled from the targeting
	// engine's Valkey shards.
	Backend       string
	RedisAddr     string
	RedisDB       int
	RedisUsername string
	RedisPassword string
	RedisTLS      bool

	// KeyPrefix scopes registry keys inside the chosen backend so
	// multiple deployments can share a single instance.
	KeyPrefix string
}

const (
	registryBackendMemory = "memory"
	registryBackendRedis  = "redis"

	defaultRegistryPollInterval   = 30 * time.Second
	defaultRegistryBootstrapLimit = 10000
	defaultRegistryFeedLimit      = 1000
)

func loadRegistryConfigFromEnv() (registryConfig, error) {
	var errs []error
	enabled, err := lookupBool("REGISTRY_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}
	if !enabled {
		return registryConfig{Enabled: false}, errors.Join(errs...)
	}

	poll, err := lookupDuration("REGISTRY_POLL_INTERVAL", defaultRegistryPollInterval)
	if err != nil {
		errs = append(errs, err)
	}
	feedLimit, err := lookupInt("REGISTRY_FEED_LIMIT", defaultRegistryFeedLimit)
	if err != nil {
		errs = append(errs, err)
	}
	bootstrap, err := lookupInt("REGISTRY_BOOTSTRAP_LIMIT", defaultRegistryBootstrapLimit)
	if err != nil {
		errs = append(errs, err)
	}

	backend := strings.ToLower(strings.TrimSpace(os.Getenv("REGISTRY_STORE_BACKEND")))
	if backend == "" {
		backend = registryBackendMemory
	}

	cfg := registryConfig{
		Enabled:        true,
		FeedURL:        strings.TrimSpace(os.Getenv("REGISTRY_FEED_URL")),
		FeedToken:      os.Getenv("REGISTRY_FEED_TOKEN"),
		PollInterval:   poll,
		FeedLimit:      feedLimit,
		BootstrapLimit: bootstrap,
		CursorPath:     strings.TrimSpace(os.Getenv("REGISTRY_CURSOR_PATH")),
		Backend:        backend,
		KeyPrefix:      strings.TrimSpace(os.Getenv("REGISTRY_KEY_PREFIX")),
	}

	if backend == registryBackendRedis {
		db, err := lookupInt("REGISTRY_REDIS_DB", 0)
		if err != nil {
			errs = append(errs, err)
		}
		tlsOn, err := lookupBool("REGISTRY_REDIS_TLS", false)
		if err != nil {
			errs = append(errs, err)
		}
		cfg.RedisAddr = strings.TrimSpace(os.Getenv("REGISTRY_REDIS_ADDR"))
		cfg.RedisDB = db
		cfg.RedisUsername = strings.TrimSpace(os.Getenv("REGISTRY_REDIS_USERNAME"))
		cfg.RedisPassword = os.Getenv("REGISTRY_REDIS_PASSWORD")
		cfg.RedisTLS = tlsOn
	}

	return cfg, errors.Join(errs...)
}

func (c registryConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if c.FeedURL == "" {
		errs = append(errs, errors.New("REGISTRY_FEED_URL is required when REGISTRY_ENABLED=true"))
	}
	if c.PollInterval <= 0 {
		errs = append(errs, errors.New("REGISTRY_POLL_INTERVAL must be positive"))
	}
	switch c.Backend {
	case registryBackendMemory:
	case registryBackendRedis:
		if c.RedisAddr == "" {
			errs = append(errs, errors.New("REGISTRY_REDIS_ADDR is required when REGISTRY_STORE_BACKEND=redis"))
		}
	default:
		errs = append(errs, fmt.Errorf("REGISTRY_STORE_BACKEND %q is not supported (use memory or redis)", c.Backend))
	}
	return errors.Join(errs...)
}

// --- env helpers (local to keep this file self-contained; the
// contextagent package owns its own copies but they are unexported).

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
