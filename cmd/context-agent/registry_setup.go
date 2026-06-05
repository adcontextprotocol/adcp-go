package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/adcontextprotocol/adcp-go/registry"
	registryredis "github.com/adcontextprotocol/adcp-go/registry/redisstore"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/contextagent"
)

// registrySyncStaleThreshold is the wall-clock budget after which the
// /live check flips to 503 if no sync iteration has completed
// successfully. Sized to ~2× the default REGISTRY_POLL_INTERVAL of
// 30s so a single missed cycle does not page; operators running with
// a longer poll interval should adjust accordingly.
const registrySyncStaleThreshold = 5 * time.Minute

// registryBundle owns the registry-side dependencies the context-agent
// uses to keep its global property bitmap fresh.
type registryBundle struct {
	properties *registry.PropertyIndex
	syncer     *registry.Syncer

	cancel   context.CancelFunc
	syncDone chan struct{}
	syncErr  atomic.Pointer[error]

	// lastSuccess holds the Unix-second timestamp of the most
	// recent successful poll (zero-event polls included). Updated
	// via the Syncer's OnSuccessfulPoll callback so a quiescent
	// feed still proves liveness even when the index is unchanged.
	lastSuccess atomic.Int64

	redisCloser interface{ Close() error }
	logger      *slog.Logger
}

// buildRegistry constructs the index + (optional) persistent store +
// feed syncer. Hydrate runs synchronously when a persistent store is
// attached so the first request after the bundle returns finds a
// populated index; the feed loop then runs in the background under
// the returned bundle's cancel.
func buildRegistry(ctx context.Context, cfg registryConfig, logger *slog.Logger) (*registryBundle, error) {
	properties := registry.NewPropertyIndex()
	auth := registry.NewAuthIndex()
	agents := registry.NewAgentIndex()

	var (
		store       registry.Store
		cursorStore registry.CursorStore
		redisCloser interface{ Close() error }
	)

	switch cfg.Backend {
	case registryBackendMemory:
		cursorStore = &registry.MemoryCursorStore{}
	case registryBackendRedis:
		client := redis.NewClient(&redis.Options{
			Addr:      cfg.RedisAddr,
			Username:  cfg.RedisUsername,
			Password:  cfg.RedisPassword,
			DB:        cfg.RedisDB,
			TLSConfig: redisTLSConfig(cfg.RedisTLS),
		})
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("registry redis ping: %w", err)
		}
		redisCloser = client
		store = registryredis.New(client, registryredis.Options{KeyPrefix: cfg.KeyPrefix})
		cursorStore = store
		properties = properties.WithStore(store)
		auth = auth.WithStore(store)
		agents = agents.WithStore(store)
	default:
		return nil, fmt.Errorf("unsupported registry backend %q", cfg.Backend)
	}

	// A file-backed cursor overrides the backend's own cursor store.
	// With the memory backend (no persistent index) this means a
	// restart resumes from the saved cursor against an EMPTY index and
	// catches up incrementally from there, rather than re-bootstrapping
	// the full feed — the index only holds entities the feed emits
	// after the resumed cursor. Pair a file cursor with the redis
	// backend (which persists the index too) when a true warm resume is
	// wanted; the memory + file-cursor combination is for resuming a
	// feed position without paying for a persistent index.
	if cfg.CursorPath != "" {
		cursorStore = registry.NewFileCursorStore(cfg.CursorPath)
	}

	if err := properties.Hydrate(ctx); err != nil {
		closeRedis(redisCloser)
		return nil, fmt.Errorf("registry property hydrate: %w", err)
	}
	if err := auth.Hydrate(ctx); err != nil {
		closeRedis(redisCloser)
		return nil, fmt.Errorf("registry auth hydrate: %w", err)
	}
	if err := agents.Hydrate(ctx); err != nil {
		closeRedis(redisCloser)
		return nil, fmt.Errorf("registry agent hydrate: %w", err)
	}

	syncCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	bundle := &registryBundle{
		properties:  properties,
		cancel:      cancel,
		syncDone:    done,
		redisCloser: redisCloser,
		logger:      logger,
	}
	// Seed lastSuccess so the staleness check has a sensible starting
	// point — the index is populated (Hydrate ran) even if no feed
	// poll has completed yet.
	bundle.lastSuccess.Store(time.Now().Unix())

	client := registry.NewClient(cfg.FeedURL, cfg.FeedToken)
	bundle.syncer = registry.NewSyncer(client, properties, auth, agents, cursorStore, registry.SyncerConfig{
		PollInterval:   cfg.PollInterval,
		FeedLimit:      cfg.FeedLimit,
		BootstrapLimit: cfg.BootstrapLimit,
		OnSuccessfulPoll: func(_ int) {
			bundle.lastSuccess.Store(time.Now().Unix())
		},
	})

	go bundle.runSyncer(syncCtx)

	if count := properties.Count(); count == 0 {
		logger.Warn("registry property index is empty after hydrate; the agent will short-circuit every request until the feed bootstrap catches up",
			"feed_url", cfg.FeedURL, "backend", cfg.Backend)
	} else {
		logger.Info("registry property index hydrated", "count", count, "backend", cfg.Backend)
	}

	return bundle, nil
}

// runSyncer wraps registry.Syncer.Run with panic recovery. The
// OnSuccessfulPoll callback wired into SyncerConfig handles success
// tracking directly, so liveness sees a heartbeat on every clean poll
// (zero-event polls included) without a separate ticker goroutine.
// A panic in the underlying HTTP client or feed decoder is captured
// into syncErr instead of taking the process down.
func (b *registryBundle) runSyncer(ctx context.Context) {
	defer close(b.syncDone)
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			err := fmt.Errorf("registry sync goroutine panic: %v\n%s", r, stack)
			if b.logger != nil {
				b.logger.Error("registry sync goroutine panicked",
					"recover", r,
					"stack", string(stack),
				)
			}
			b.syncErr.Store(&err)
		}
	}()

	err := b.syncer.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		if b.logger != nil {
			b.logger.Error("registry sync loop exited", "error", err)
		}
		b.syncErr.Store(&err)
	}
}

// Shutdown cancels the sync loop, waits for it to exit, and closes
// the underlying Redis client (when one was opened). Idempotent:
// repeat calls are safe because cancel is idempotent, the done
// channel is read once-and-done, and closeRedis tolerates a second
// Close.
func (b *registryBundle) Shutdown() {
	if b == nil {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.syncDone != nil {
		<-b.syncDone
	}
	closeRedis(b.redisCloser)
}

// LivenessCheck returns a contextagent.LivenessCheck that flips to
// 503 when (a) the sync goroutine has exited (with or without
// error), or (b) no successful sync iteration has been observed
// within registrySyncStaleThreshold. The probe deliberately reads
// stale state from the goroutine via atomic helpers — no shared
// locks on the request path.
func (b *registryBundle) LivenessCheck() contextagent.LivenessCheck {
	return contextagent.LivenessCheck{
		Name: "registry_sync",
		Fn: func() error {
			if b == nil {
				return errors.New("registry bundle not initialized")
			}
			select {
			case <-b.syncDone:
				err := b.syncErr.Load()
				if err != nil && *err != nil {
					return fmt.Errorf("registry sync goroutine exited: %w", *err)
				}
				return errors.New("registry sync goroutine exited")
			default:
			}
			last := time.Unix(b.lastSuccess.Load(), 0)
			if age := time.Since(last); age > registrySyncStaleThreshold {
				return fmt.Errorf("registry sync has not progressed in %s (>%s)", age.Round(time.Second), registrySyncStaleThreshold)
			}
			return nil
		},
	}
}

// PropertyBitmap adapts the registry's PropertyIndex into the
// targeting.Bitmap surface the context engine consults at request
// time. The bitmap is dynamic: every Contains call queries the live
// index, so syncer updates take effect without a rebuild.
func (b *registryBundle) PropertyBitmap() targeting.Bitmap {
	return &registryPropertyBitmap{idx: b.properties}
}

// registryPropertyBitmap wraps a registry.PropertyIndex as a
// targeting.Bitmap. The engine passes the inbound request's
// property_rid string; the adapter looks it up against both lookup
// dimensions the index exposes so callers can match either the
// human-readable property_id slug or the numeric registry RID without
// the engine having to know which is which.
type registryPropertyBitmap struct {
	idx *registry.PropertyIndex
}

func (b *registryPropertyBitmap) Contains(rid string) bool {
	if b == nil || b.idx == nil || rid == "" {
		return false
	}
	if _, ok := b.idx.LookupByID(rid); ok {
		return true
	}
	n, err := strconv.ParseUint(rid, 10, 64)
	if err != nil {
		return false
	}
	_, ok := b.idx.LookupByRID(n)
	return ok
}

func redisTLSConfig(enabled bool) *tls.Config {
	if !enabled {
		return nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

func closeRedis(c interface{ Close() error }) {
	if c == nil {
		return
	}
	_ = c.Close()
}
