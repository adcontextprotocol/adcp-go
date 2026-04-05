package targeting

import (
	"context"
	"fmt"
	"time"
)

// isPropertySuppressed checks if a property RID is suppressed for this provider.
func (e *Engine) isPropertySuppressed(ctx context.Context, rid uint64) (bool, error) {
	key := fmt.Sprintf("suppress:%s:property:%d", e.providerID, rid)
	return e.store.Exists(ctx, key)
}

// isGeoSuppressed checks if a country is suppressed for this provider.
func (e *Engine) isGeoSuppressed(ctx context.Context, countryCode string) (bool, error) {
	if countryCode == "" {
		return false, nil
	}
	key := fmt.Sprintf("suppress:%s:geo:%s", e.providerID, countryCode)
	return e.store.Exists(ctx, key)
}

// SuppressProperty adds a property suppression with a TTL.
func (e *Engine) SuppressProperty(ctx context.Context, rid uint64, ttl time.Duration) error {
	key := fmt.Sprintf("suppress:%s:property:%d", e.providerID, rid)
	return e.store.Set(ctx, key, "1", ttl)
}

// SuppressGeo adds a geo suppression with a TTL.
func (e *Engine) SuppressGeo(ctx context.Context, countryCode string, ttl time.Duration) error {
	key := fmt.Sprintf("suppress:%s:geo:%s", e.providerID, countryCode)
	return e.store.Set(ctx, key, "1", ttl)
}
