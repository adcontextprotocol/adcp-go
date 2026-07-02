package pkgconfigstore

import (
	"context"
	"encoding/json"
	"maps"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
)

// CacheConfig sizes the per-package config cache.
type CacheConfig struct {
	Size int
	TTL  time.Duration
}

// WithCache wraps r in an LRU cache. Negative results are cached under
// the same TTL.
func WithCache(r Reader, cfg CacheConfig) Reader {
	if r == nil {
		return nil
	}
	return &cachedReader{
		inner:   r,
		entries: lru.NewLRU[string, cachedEntry](cfg.Size, nil, cfg.TTL),
	}
}

type cachedEntry struct {
	Config  *targeting.PackageContextConfig
	Present bool
}

type cachedReader struct {
	inner   Reader
	entries *lru.LRU[string, cachedEntry]
}

func (c *cachedReader) Get(ctx context.Context, packageID string) (*targeting.PackageContextConfig, bool, error) {
	if hit, ok := c.entries.Get(packageID); ok {
		return clonePackageContextConfig(hit.Config), hit.Present, nil
	}
	cfg, present, err := c.inner.Get(ctx, packageID)
	if err != nil {
		return nil, false, err
	}
	// Defense-in-depth: clone before insert too. The current direct
	// reader unmarshals a fresh struct on every call, so the pointer
	// returned by inner.Get is already exclusive to this call. A
	// future intermediated reader (e.g. another layer of caching, a
	// pool of decoded configs) could break that exclusivity and let
	// our caller's mutation race with our own stored pointer. Clone
	// both directions so the cache contract is "never share pointers"
	// regardless of what inner does.
	stored := clonePackageContextConfig(cfg)
	c.entries.Add(packageID, cachedEntry{Config: stored, Present: present})
	return clonePackageContextConfig(stored), present, nil
}

// MGet serves cache hits from memory and batches the remaining misses
// into one inner MGet round-trip. Result alignment to packageIDs is
// preserved.
func (c *cachedReader) MGet(ctx context.Context, packageIDs []string) ([]*targeting.PackageContextConfig, error) {
	if len(packageIDs) == 0 {
		return nil, nil
	}
	out := make([]*targeting.PackageContextConfig, len(packageIDs))
	missIdx := make([]int, 0, len(packageIDs))
	missIDs := make([]string, 0, len(packageIDs))
	for i, id := range packageIDs {
		if hit, ok := c.entries.Get(id); ok {
			if hit.Present {
				out[i] = clonePackageContextConfig(hit.Config)
			}
			continue
		}
		missIdx = append(missIdx, i)
		missIDs = append(missIDs, id)
	}
	if len(missIDs) == 0 {
		return out, nil
	}
	fetched, err := c.inner.MGet(ctx, missIDs)
	if err != nil {
		return nil, err
	}
	for j, cfg := range fetched {
		i := missIdx[j]
		stored := clonePackageContextConfig(cfg)
		c.entries.Add(packageIDs[i], cachedEntry{Config: stored, Present: cfg != nil})
		if cfg != nil {
			out[i] = clonePackageContextConfig(stored)
		}
	}
	return out, nil
}

// clonePackageContextConfig returns an independent copy of cfg with
// every slice / map / json.RawMessage field freshly allocated so a
// caller mutating an Offer, a PropertyRID, or any other contained
// slice/map cannot poison the cached pointer that subsequent requests
// receive. Returns nil when cfg is nil (negative-cache hit).
//
// The unexported PackageContextConfig.propertyRIDBitmap is intentionally
// SHARED across clones — the `out := *cfg` memcopy propagates the
// interface pointer, and the bitmap is treated as immutable after
// MaterializePropertyBitmap (see the field comment in targeting/entity.go
// for the full invariant). PropertyRIDs is deep-copied below to keep
// slice isolation for callers, and the two stay consistent only because
// nothing mutates the reallocated slice's contents. If a future edit
// starts writing into PropertyRIDs in place, that call site MUST
// re-materialize the bitmap or the gate will go stale.
func clonePackageContextConfig(cfg *targeting.PackageContextConfig) *targeting.PackageContextConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	if len(cfg.PropertyRIDs) > 0 {
		out.PropertyRIDs = append([]string(nil), cfg.PropertyRIDs...)
	}
	if len(cfg.EmitSegments) > 0 {
		out.EmitSegments = append([]string(nil), cfg.EmitSegments...)
	}
	if len(cfg.Offers) > 0 {
		out.Offers = make([]targeting.OfferConfigJSON, len(cfg.Offers))
		for i, o := range cfg.Offers {
			out.Offers[i] = o
			if len(o.Brand) > 0 {
				out.Offers[i].Brand = append(json.RawMessage(nil), o.Brand...)
			}
			if len(o.Macros) > 0 {
				macros := make(map[string]string, len(o.Macros))
				maps.Copy(macros, o.Macros)
				out.Offers[i].Macros = macros
			}
		}
	}
	if len(cfg.Brand) > 0 {
		out.Brand = append(json.RawMessage(nil), cfg.Brand...)
	}
	if len(cfg.CreativeManifest) > 0 {
		out.CreativeManifest = append(json.RawMessage(nil), cfg.CreativeManifest...)
	}
	if len(cfg.Macros) > 0 {
		macros := make(map[string]string, len(cfg.Macros))
		maps.Copy(macros, cfg.Macros)
		out.Macros = macros
	}
	out.ContextSignals = cloneSignalProfile(cfg.ContextSignals)
	return &out
}

func cloneSignalProfile(p *signalstore.Profile) *signalstore.Profile {
	if p == nil {
		return nil
	}
	out := &signalstore.Profile{}
	if len(p.AnyOf) > 0 {
		out.AnyOf = make([]signalstore.Cfg, len(p.AnyOf))
		for i, c := range p.AnyOf {
			out.AnyOf[i] = cloneSignalCfg(c)
		}
	}
	if len(p.NoneOf) > 0 {
		out.NoneOf = make([]signalstore.Cfg, len(p.NoneOf))
		for i, c := range p.NoneOf {
			out.NoneOf[i] = cloneSignalCfg(c)
		}
	}
	return out
}

func cloneSignalCfg(c signalstore.Cfg) signalstore.Cfg {
	out := c
	if len(c.KeyTypes) > 0 {
		out.KeyTypes = append([]signalstore.KeyType(nil), c.KeyTypes...)
	}
	return out
}
