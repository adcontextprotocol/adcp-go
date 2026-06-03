package pkgconfigstore

import (
	"context"
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
		return hit.Config, hit.Present, nil
	}
	cfg, present, err := c.inner.Get(ctx, packageID)
	if err != nil {
		return nil, false, err
	}
	c.entries.Add(packageID, cachedEntry{Config: cfg, Present: present})
	return cfg, present, nil
}
