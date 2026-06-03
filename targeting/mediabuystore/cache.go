package mediabuystore

import (
	"context"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// CacheConfig sizes the two LRU caches the wrapper maintains:
// the seller→media-buy-id set, and the media-buy-id→record map.
// ActivePackages is derived from these two and is not cached
// separately — its result depends on `now`, which would invalidate
// every entry after one TTL anyway.
type CacheConfig struct {
	// SellerSetSize bounds the number of distinct seller_agent_url
	// entries held in the seller-set cache. Each entry is a slice of
	// media_buy_id strings.
	SellerSetSize int
	// SellerSetTTL is the lifetime of one seller-set cache entry.
	SellerSetTTL time.Duration

	// MediaBuySize bounds the number of distinct media-buy records
	// held in the per-buy cache.
	MediaBuySize int
	// MediaBuyTTL is the lifetime of one per-buy cache entry.
	MediaBuyTTL time.Duration
}

// WithCache wraps r in an LRU cache layered on the two underlying read
// keys (seller-set and per-buy record). Negative results are cached
// under the same TTL so a miss does not re-hit the backing store on
// every request.
func WithCache(r Reader, cfg CacheConfig) Reader {
	if r == nil {
		return nil
	}
	return &cachedReader{
		inner:    r,
		sellers:  lru.NewLRU[string, cachedSellerSet](cfg.SellerSetSize, nil, cfg.SellerSetTTL),
		buys:     lru.NewLRU[string, cachedMediaBuy](cfg.MediaBuySize, nil, cfg.MediaBuyTTL),
	}
}

type cachedSellerSet struct {
	IDs     []string
	Present bool
}

type cachedMediaBuy struct {
	MB      MediaBuy
	Present bool
}

type cachedReader struct {
	inner   Reader
	sellers *lru.LRU[string, cachedSellerSet]
	buys    *lru.LRU[string, cachedMediaBuy]
}

func (c *cachedReader) MediaBuyIDsForSeller(ctx context.Context, sellerAgentURL string) ([]string, error) {
	if hit, ok := c.sellers.Get(sellerAgentURL); ok {
		if !hit.Present {
			return nil, nil
		}
		// Return a copy so cache contents stay immutable.
		out := make([]string, len(hit.IDs))
		copy(out, hit.IDs)
		return out, nil
	}
	ids, err := c.inner.MediaBuyIDsForSeller(ctx, sellerAgentURL)
	if err != nil {
		return nil, err
	}
	cp := make([]string, len(ids))
	copy(cp, ids)
	c.sellers.Add(sellerAgentURL, cachedSellerSet{IDs: cp, Present: len(ids) > 0})
	return ids, nil
}

func (c *cachedReader) MediaBuy(ctx context.Context, mediaBuyID string) (MediaBuy, bool, error) {
	if hit, ok := c.buys.Get(mediaBuyID); ok {
		return hit.MB, hit.Present, nil
	}
	mb, present, err := c.inner.MediaBuy(ctx, mediaBuyID)
	if err != nil {
		return MediaBuy{}, false, err
	}
	c.buys.Add(mediaBuyID, cachedMediaBuy{MB: mb, Present: present})
	return mb, present, nil
}

func (c *cachedReader) ActivePackages(ctx context.Context, sellerAgentURL, propertyID, country string, now time.Time) ([]MediaBuyPackage, error) {
	ids, err := c.MediaBuyIDsForSeller(ctx, sellerAgentURL)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var (
		out     []MediaBuyPackage
		orphans bool
	)
	for _, id := range ids {
		mb, ok, err := c.MediaBuy(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The seller-set still claims this id, but the per-buy
			// record is gone. Could be a stale seller-set cache
			// entry, or a writer that removed the buy without
			// SREM-ing the seller-set. Self-heal by evicting the
			// seller-set so the next request re-fetches the truth.
			orphans = true
			continue
		}
		if !isActive(mb, now) || !matchesGeo(mb, country) || !matchesProperty(mb, propertyID) {
			continue
		}
		out = append(out, mb.Packages...)
	}
	if orphans {
		c.sellers.Remove(sellerAgentURL)
	}
	return out, nil
}
