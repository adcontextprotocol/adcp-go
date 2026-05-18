package redisstore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Mode selects the connection topology.
//
//   - ModeStandalone — a single Valkey endpoint. Uses go-redis Client with
//     ValkeyDB for logical-DB selection. Shards["0"] is the endpoint.
//   - ModeCluster — Valkey Cluster. Uses go-redis ClusterClient bootstrapped
//     from every endpoint in Shards. Real slot ownership is discovered via
//     CLUSTER SLOTS at connect time; the Shards keying is purely bootstrap.
//   - ModeShadow — N independent standalone replicas mirroring a cluster's
//     per-shard keyspace. App-level CRC16 routing picks a shard per key.
//     Shards must be a contiguous "0".."N-1" sequence; ordinal order is
//     load-bearing for routing.
type Mode string

const (
	ModeStandalone Mode = "standalone"
	ModeCluster    Mode = "cluster"
	ModeShadow     Mode = "shadow"
)

// IsValid reports whether the value is one of the supported modes.
func (m Mode) IsValid() bool {
	switch m {
	case ModeStandalone, ModeCluster, ModeShadow:
		return true
	}
	return false
}

// Config is the input to Build. Shards is a map keyed by shard ordinal
// (string) → "host:port". The keying meaning depends on Mode — see Mode for
// the per-mode contract.
type Config struct {
	Mode     Mode
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

// Validate reports the first configuration error or nil. It does not open
// any connections.
func (c Config) Validate() error {
	if !c.Mode.IsValid() {
		return fmt.Errorf("redisstore: invalid mode %q (want one of %q, %q, %q)",
			c.Mode, ModeStandalone, ModeCluster, ModeShadow)
	}
	if len(c.Shards) == 0 {
		return errors.New("redisstore: shards must contain at least one entry")
	}
	for k, v := range c.Shards {
		if v == "" {
			return fmt.Errorf("redisstore: shards[%q] is empty", k)
		}
	}
	switch c.Mode {
	case ModeStandalone:
		if _, ok := c.Shards["0"]; !ok {
			return errors.New(`redisstore: mode=standalone requires shards to contain key "0"`)
		}
	case ModeShadow:
		ordinals := make([]int, 0, len(c.Shards))
		for k := range c.Shards {
			ord, err := strconv.Atoi(k)
			if err != nil {
				return fmt.Errorf("redisstore: mode=shadow shard key %q is not an integer ordinal", k)
			}
			if ord < 0 {
				return fmt.Errorf("redisstore: mode=shadow shard key %q is negative", k)
			}
			ordinals = append(ordinals, ord)
		}
		sort.Ints(ordinals)
		for i, ord := range ordinals {
			if ord != i {
				return fmt.Errorf("redisstore: mode=shadow shard ordinals must be contiguous 0..%d, got %v", len(ordinals)-1, ordinals)
			}
		}
	}
	return nil
}

// Build constructs a *Store ready to satisfy targeting.ContextStore,
// fcap.Store, and audience.Store. The returned io.Closer releases every
// underlying connection pool on shutdown.
//
// Build performs a bounded PING against each underlying client before
// returning, so a misconfigured endpoint surfaces at startup rather than on
// the first request. The PING uses Config.DialTimeout (or 5s when unset).
func Build(ctx context.Context, cfg Config) (*Store, io.Closer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	switch cfg.Mode {
	case ModeStandalone:
		client, closer, err := buildStandalone(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		return New(client), closer, nil
	case ModeCluster:
		client, closer, err := buildCluster(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		return New(client), closer, nil
	case ModeShadow:
		store, closer, err := buildShadow(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		return store, closer, nil
	}
	return nil, nil, fmt.Errorf("redisstore: unsupported mode %q", cfg.Mode)
}

func buildStandalone(ctx context.Context, cfg Config) (redis.UniversalClient, io.Closer, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Shards["0"],
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolSize:     cfg.PoolSize,
		TLSConfig:    tlsConfig(cfg),
	})
	if err := ping(ctx, client, cfg.DialTimeout); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("redisstore: standalone ping: %w", err)
	}
	return client, client, nil
}

func buildCluster(ctx context.Context, cfg Config) (redis.UniversalClient, io.Closer, error) {
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        sortedValues(cfg.Shards),
		Username:     cfg.Username,
		Password:     cfg.Password,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolSize:     cfg.PoolSize,
		TLSConfig:    tlsConfig(cfg),
	})
	if err := ping(ctx, client, cfg.DialTimeout); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("redisstore: cluster ping: %w", err)
	}
	return client, client, nil
}

func buildShadow(ctx context.Context, cfg Config) (*Store, io.Closer, error) {
	ordinals := make([]int, 0, len(cfg.Shards))
	for k := range cfg.Shards {
		ord, _ := strconv.Atoi(k) // validated in Config.Validate
		ordinals = append(ordinals, ord)
	}
	sort.Ints(ordinals)

	clients := make([]*redis.Client, len(ordinals))
	for i, ord := range ordinals {
		clients[i] = redis.NewClient(&redis.Options{
			Addr:         cfg.Shards[strconv.Itoa(ord)],
			Username:     cfg.Username,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  cfg.DialTimeout,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			PoolSize:     cfg.PoolSize,
			TLSConfig:    tlsConfig(cfg),
		})
	}
	for i, c := range clients {
		if err := ping(ctx, c, cfg.DialTimeout); err != nil {
			closeClients(clients)
			return nil, nil, fmt.Errorf("redisstore: shadow shard %d ping: %w", i, err)
		}
	}
	store, err := NewShadow(clients)
	if err != nil {
		closeClients(clients)
		return nil, nil, err
	}
	return store, shadowCloser(clients), nil
}

func ping(ctx context.Context, client redis.UniversalClient, dialTimeout time.Duration) error {
	timeout := dialTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return client.Ping(pingCtx).Err()
}

func tlsConfig(cfg Config) *tls.Config {
	if !cfg.TLS {
		return nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// sortedValues returns the endpoint list ordered by integer ordinal when the
// shard keys parse as integers, otherwise by the string key. Stable ordering
// matters for cluster clients that surface the first listed endpoint in
// errors and metrics.
func sortedValues(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, errA := strconv.Atoi(keys[i])
		b, errB := strconv.Atoi(keys[j])
		if errA == nil && errB == nil {
			return a < b
		}
		return keys[i] < keys[j]
	})
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

func closeClients(clients []*redis.Client) {
	for _, c := range clients {
		if c != nil {
			_ = c.Close()
		}
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func shadowCloser(clients []*redis.Client) io.Closer {
	return closerFunc(func() error {
		var firstErr error
		for _, c := range clients {
			if err := c.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	})
}
