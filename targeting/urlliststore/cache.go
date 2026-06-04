package urlliststore

import (
	"context"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// CacheConfig sizes the URL membership cache. The wrapper holds two
// internal LRUs — one for blocked-verdicts, one for allowed-verdicts —
// each sized at Size and each with TTL. Memory footprint is therefore
// roughly 2 × Size cache entries, not Size.
type CacheConfig struct {
	Size int
	TTL  time.Duration
}

// WithCache wraps r in an LRU cache. Negative results are cached.
func WithCache(r Reader, cfg CacheConfig) Reader {
	if r == nil {
		return nil
	}
	return &cachedReader{
		inner:    r,
		blocked:  lru.NewLRU[string, bool](cfg.Size, nil, cfg.TTL),
		allowed:  lru.NewLRU[string, bool](cfg.Size, nil, cfg.TTL),
	}
}

type cachedReader struct {
	inner   Reader
	blocked *lru.LRU[string, bool]
	allowed *lru.LRU[string, bool]
}

func cacheKey(packageID, urlHash string) string { return packageID + "\x00" + urlHash }

func (c *cachedReader) IsBlocked(ctx context.Context, packageID, urlHash string) (bool, error) {
	k := cacheKey(packageID, urlHash)
	if v, ok := c.blocked.Get(k); ok {
		return v, nil
	}
	v, err := c.inner.IsBlocked(ctx, packageID, urlHash)
	if err != nil {
		return false, err
	}
	c.blocked.Add(k, v)
	return v, nil
}

func (c *cachedReader) IsAllowed(ctx context.Context, packageID, urlHash string) (bool, error) {
	k := cacheKey(packageID, urlHash)
	if v, ok := c.allowed.Get(k); ok {
		return v, nil
	}
	v, err := c.inner.IsAllowed(ctx, packageID, urlHash)
	if err != nil {
		return false, err
	}
	c.allowed.Add(k, v)
	return v, nil
}
