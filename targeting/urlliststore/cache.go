package urlliststore

import (
	"context"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// CacheConfig sizes the URL membership cache. One LRU holds tuples of
// (packageID, hash) → blocked/allowed verdicts. The same cache size /
// TTL covers both blocklist and allowlist verdicts; callers that need
// separate sizing can construct two readers and wire them in
// independently.
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
