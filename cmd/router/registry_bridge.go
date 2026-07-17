package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/adcontextprotocol/adcp-go/registry"
	"github.com/adcontextprotocol/adcp-go/router"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// registryBridge owns the AdCP registry feed integration. It runs a
// registry.Syncer against the configured feed URL, keeps a local
// registry.PropertyIndex hydrated, and mirrors every successful poll into
// the router's in-process router.Registry so `/registry/snapshot` serves
// live property metadata to downstream providers.
//
// The router.Registry is the wire-serving layer; registry.PropertyIndex is
// the polling layer. They are kept in sync by rebuildRouterSnapshot, which
// runs on every OnSuccessfulPoll callback. Feed properties AND the
// router's authorized-RID signing records are assembled into one slice
// before a single LoadFromData call — a two-phase rebuild would leave a
// window where /registry/snapshot serves feed properties without the
// router's own signing keys and downstream providers would cache that
// keyless snapshot and reject legitimate router-signed requests.
type registryBridge struct {
	client     *registry.Client
	properties *registry.PropertyIndex
	// AuthIndex and AgentIndex are hydrated because registry.Syncer requires
	// them, but they are intentionally not projected into router.Registry —
	// downstream providers consume auth/agent facts through separate
	// registry channels (LazyAuthorizationKeyStore), not through
	// /registry/snapshot.
	auth   *registry.AuthIndex
	agents *registry.AgentIndex
	cursor registry.CursorStore
	syncer *registry.Syncer

	router *router.Registry

	// Signing-key projection: on every rebuild, attach routerKey to the
	// records for each authorizedRID (merging into a feed-provided record
	// when present, adding a placeholder otherwise). When no signer is
	// configured, both are zero-value and no keys are projected.
	authorizedRIDs []string
	routerKey      tmproto.SigningKey
	hasSigner      bool

	cancel   context.CancelFunc
	syncDone chan struct{}

	logger *slog.Logger
}

// buildRegistryBridge wires the syncer into a router.Registry so
// /registry/snapshot serves live property metadata from the AdCP feed.
// Returns (nil, nil) when cfg.Enabled() is false so the caller can
// continue with the seed-only fallback.
func buildRegistryBridge(cfg router.RegistryConfig, rtr *router.Registry, authorizedRIDs []string, routerKey tmproto.SigningKey, hasSigner bool, logger *slog.Logger) (*registryBridge, error) {
	if !cfg.Enabled() {
		return nil, nil
	}

	client := registry.NewClient(cfg.FeedURL, cfg.FeedToken)
	properties := registry.NewPropertyIndex()
	auth := registry.NewAuthIndex()
	agents := registry.NewAgentIndex()
	cursor := &registry.MemoryCursorStore{}

	// Hydrate empty indexes so Put semantics are honored during the first
	// bootstrap page — matches the reference agents. The in-memory indexes
	// have no persistent backend to load from, so this is a no-op today,
	// but it keeps the invariant Syncer relies on.
	ctx := context.Background()
	if err := properties.Hydrate(ctx); err != nil {
		return nil, fmt.Errorf("registry property hydrate: %w", err)
	}
	if err := auth.Hydrate(ctx); err != nil {
		return nil, fmt.Errorf("registry auth hydrate: %w", err)
	}
	if err := agents.Hydrate(ctx); err != nil {
		return nil, fmt.Errorf("registry agent hydrate: %w", err)
	}

	b := &registryBridge{
		client:         client,
		properties:     properties,
		auth:           auth,
		agents:         agents,
		cursor:         cursor,
		router:         rtr,
		authorizedRIDs: append([]string(nil), authorizedRIDs...),
		routerKey:      routerKey,
		hasSigner:      hasSigner,
		logger:         logger,
	}

	b.syncer = registry.NewSyncer(client, properties, auth, agents, cursor, registry.SyncerConfig{
		PollInterval:   cfg.PollInterval(),
		FeedLimit:      cfg.FeedLimit,
		BootstrapLimit: cfg.BootstrapLimit,
		OnSuccessfulPoll: func(_ int) {
			b.rebuildRouterSnapshot()
		},
	})

	syncCtx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.syncDone = make(chan struct{})
	go b.runSyncer(syncCtx)

	logger.Info("registry sync started",
		"feed_url", cfg.FeedURL,
		"poll_interval", cfg.PollInterval(),
	)
	return b, nil
}

// runSyncer wraps registry.Syncer.Run with panic recovery so a decoder or
// HTTP transport failure inside the feed loop cannot take the router down.
// Failures are logged; the router keeps serving the last-good snapshot
// (potentially indefinitely) until /healthz wiring lands and surfaces the
// stalled sync to an orchestrator.
func (b *registryBridge) runSyncer(ctx context.Context) {
	defer close(b.syncDone)
	defer func() {
		if r := recover(); r != nil {
			if b.logger != nil {
				b.logger.Error("registry sync goroutine panicked",
					"recover", r,
					"stack", string(debug.Stack()),
				)
			}
		}
	}()

	err := b.syncer.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		if b.logger != nil {
			b.logger.Error("registry sync loop exited", "error", err)
		}
	}
}

// rebuildRouterSnapshot atomically replaces the wire-serving snapshot with
// (a) every feed-provided property and (b) the router's own signing key
// attached to each authorized RID. Called on every OnSuccessfulPoll.
//
// The two-list merge happens BEFORE LoadFromData so the single map swap
// inside applySnapshot publishes both feed properties and the router
// signing key together. A prior version did two locked phases —
// LoadFromData then per-RID AttachSigningKey — which left a window where
// /registry/snapshot served feed properties without the router key, and
// downstream providers that fetched during that window cached a keyless
// snapshot and rejected legitimate router-signed requests until their next
// poll.
//
// The Property → RegistryProperty projection is intentionally hand-copied
// rather than reflected: any new field added to registry.Property would be
// silently dropped, which is the correct default (fields need explicit
// audit before crossing into the router's wire-serving surface). Feed
// signing keys are deliberately NOT projected — a compromised feed must
// not be able to inject a signing key onto the router's snapshot.
func (b *registryBridge) rebuildRouterSnapshot() {
	if b == nil || b.router == nil {
		return
	}

	src := b.properties.All()
	// Cap hint sized to the feed alone; the authorized-RID tail is small
	// (operator-configured, typically single-digit) so append growth is
	// negligible and adding the two lengths would give CodeQL a theoretical
	// overflow flag on an allocation size.
	props := make([]router.RegistryProperty, 0, len(src))

	// Feed-provided properties first, indexed by RID so we can merge
	// signing keys into any authorized-RID record the feed also emits.
	ridToPos := make(map[string]int, len(src))
	for i := range src {
		p := src[i]
		if p.PropertyRID != "" {
			ridToPos[p.PropertyRID] = len(props)
		}
		props = append(props, router.RegistryProperty{
			PropertyID:   p.PropertyID,
			PropertyRID:  p.PropertyRID,
			PropertyType: p.PropertyType,
			Domain:       p.Domain,
			Placements:   append([]string(nil), p.Placements...),
		})
	}

	// Router's own signing key: attach to every authorized RID. When the
	// feed does not carry the RID (typical during bootstrap or for
	// operator-authorized RIDs the feed hasn't emitted yet), synthesize a
	// placeholder record so downstream providers can still resolve the
	// kid via LookupKey.
	if b.hasSigner {
		for _, rid := range b.authorizedRIDs {
			if pos, ok := ridToPos[rid]; ok {
				props[pos].SigningKeys = append(props[pos].SigningKeys, b.routerKey)
				continue
			}
			props = append(props, router.RegistryProperty{
				PropertyRID: rid,
				PropertyID:  rid, // placeholder until the feed emits the record
				SigningKeys: []tmproto.SigningKey{b.routerKey},
			})
		}
	}

	// Sequence advances by 1 per successful poll, read fresh from the
	// router so any pre-bridge init (seedSigningProperties at startup)
	// stays monotonically below every subsequent rebuild — no sequence
	// counter of our own to drift.
	seq := b.router.Sequence() + 1
	b.router.LoadFromData(props, seq)
}

// Shutdown cancels the sync loop and waits for it to exit. Idempotent.
func (b *registryBridge) Shutdown() {
	if b == nil {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.syncDone != nil {
		<-b.syncDone
	}
}
