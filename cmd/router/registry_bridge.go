package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"

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
// runs on every OnSuccessfulPoll callback. router.Registry.LoadFromData
// swaps the internal maps atomically, so any signing keys attached to
// properties before the swap are lost — reseedFn re-attaches the router's
// own signing keys after each rebuild.
type registryBridge struct {
	client     *registry.Client
	properties *registry.PropertyIndex
	auth       *registry.AuthIndex
	agents     *registry.AgentIndex
	cursor     registry.CursorStore
	syncer     *registry.Syncer

	router    *router.Registry
	reseedFn  func(*router.Registry)
	seqSource atomic.Uint64

	cancel   context.CancelFunc
	syncDone chan struct{}

	logger *slog.Logger
}

// buildRegistryBridge wires the syncer into a router.Registry so
// /registry/snapshot serves live property metadata from the AdCP feed.
// Returns (nil, nil) when cfg.Enabled() is false so the caller can
// continue with the seed-only fallback.
func buildRegistryBridge(cfg router.RegistryConfig, rtr *router.Registry, reseed func(*router.Registry), logger *slog.Logger) (*registryBridge, error) {
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
		client:     client,
		properties: properties,
		auth:       auth,
		agents:     agents,
		cursor:     cursor,
		router:     rtr,
		reseedFn:   reseed,
		logger:     logger,
	}
	// Seed the sequence source from the router's current sequence so the
	// first mirror after seedSigningProperties advances past what the seed
	// already published — otherwise downstream consumers using sequence
	// for delta tracking would observe the registry going backward.
	b.seqSource.Store(rtr.Sequence())

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

// rebuildRouterSnapshot copies the current property index into the router's
// wire-serving Registry. Called on every OnSuccessfulPoll. Signing keys
// attached to the previous snapshot are dropped by LoadFromData's map swap,
// so reseedFn re-attaches the router's own kid to its authorized properties
// after the load.
//
// The Property → RegistryProperty projection is intentionally hand-copied
// rather than reflected: any new field added to registry.Property would be
// silently dropped, which is the correct default (fields need explicit
// audit before crossing into the router's wire-serving surface). Add them
// here deliberately.
func (b *registryBridge) rebuildRouterSnapshot() {
	if b == nil || b.router == nil {
		return
	}
	src := b.properties.All()
	props := make([]router.RegistryProperty, 0, len(src))
	for i := range src {
		p := src[i]
		props = append(props, router.RegistryProperty{
			PropertyID:   p.PropertyID,
			PropertyRID:  p.PropertyRID,
			PropertyType: p.PropertyType,
			Domain:       p.Domain,
			Placements:   append([]string(nil), p.Placements...),
		})
	}
	seq := b.seqSource.Add(1)
	b.router.LoadFromData(props, seq)
	if b.reseedFn != nil {
		b.reseedFn(b.router)
	}
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

// reseedSigningPropertiesFactory builds a reseed callback that re-applies
// the router's authorized property RIDs + signing key onto every new
// snapshot. The router's own kid is the only key the router serves; other
// property owners' keys flow in via the registry feed's property records.
func reseedSigningPropertiesFactory(propertyRIDs []string, jwk tmproto.SigningKey) func(*router.Registry) {
	if len(propertyRIDs) == 0 {
		return func(*router.Registry) {}
	}
	rids := append([]string(nil), propertyRIDs...)
	return func(reg *router.Registry) {
		seedSigningProperties(reg, rids, jwk)
	}
}
