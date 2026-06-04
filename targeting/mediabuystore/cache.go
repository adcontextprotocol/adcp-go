package mediabuystore

import (
	"context"
	"errors"
	"log/slog"
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
// every request. Decode errors during ActivePackages log a Warn through
// slog.Default; callers that want a different logger should use
// WithCacheAndLogger.
func WithCache(r Reader, cfg CacheConfig) Reader {
	return WithCacheAndLogger(r, cfg, nil)
}

// WithCacheAndLogger is WithCache with an explicit slog.Logger. A nil
// logger falls back to slog.Default.
func WithCacheAndLogger(r Reader, cfg CacheConfig, logger *slog.Logger) Reader {
	if r == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &cachedReader{
		inner:   r,
		logger:  logger,
		sellers: lru.NewLRU[string, cachedSellerSet](cfg.SellerSetSize, nil, cfg.SellerSetTTL),
		buys:    lru.NewLRU[string, cachedMediaBuy](cfg.MediaBuySize, nil, cfg.MediaBuyTTL),
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
	logger  *slog.Logger
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

func (c *cachedReader) ActivePackages(ctx context.Context, sellerAgentURL, propertyID, country, placementID string, now time.Time) ([]MediaBuyPackage, error) {
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
			// Align with the direct reader: a corrupt JSON payload
			// skips one buy and logs, instead of sinking the whole
			// seller. Reader.MediaBuy wraps decode errors with
			// ErrCorruptPayload; everything else (transport,
			// canceled ctx) still propagates so the engine can map
			// to the right metric / response code.
			if errors.Is(err, ErrCorruptPayload) {
				c.logger.Warn("mediabuystore: skipping corrupt media-buy payload",
					"media_buy_id", id, "reason", "corrupt_payload", "error", err.Error())
				continue
			}
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
		for _, pkg := range mb.Packages {
			if matchesPlacement(pkg, placementID) {
				out = append(out, pkg)
			}
		}
	}
	if orphans {
		c.sellers.Remove(sellerAgentURL)
	}
	return out, nil
}
