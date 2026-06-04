package pkgconfigstore

import (
	"context"
	"encoding/json"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"

	"github.com/adcontextprotocol/adcp-go/targeting"
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

// clonePackageContextConfig returns an independent copy of cfg with
// every slice / map / json.RawMessage field freshly allocated so a
// caller mutating an Offer, a PropertyRID, or any other contained
// slice/map cannot poison the cached pointer that subsequent requests
// receive. Returns nil when cfg is nil (negative-cache hit).
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
				for k, v := range o.Macros {
					macros[k] = v
				}
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
		for k, v := range cfg.Macros {
			macros[k] = v
		}
		out.Macros = macros
	}
	return &out
}
