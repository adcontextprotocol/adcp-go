package main

import (
	"context"
	"fmt"
	"time"
)

// SuppressionManager handles property and geo suppression checks against Valkey.
// Suppressions are stored with TTLs and expire automatically.
// Key format: suppress:{provider_id}:property:{property_rid}
//             suppress:{provider_id}:geo:{country_code}
type SuppressionManager struct {
	valkey     ValkeyClient
	providerID string
}

func NewSuppressionManager(valkey ValkeyClient, providerID string) *SuppressionManager {
	return &SuppressionManager{valkey: valkey, providerID: providerID}
}

// IsPropertySuppressed checks if a property RID is suppressed for this provider.
func (s *SuppressionManager) IsPropertySuppressed(ctx context.Context, propertyRID uint32) (bool, error) {
	key := fmt.Sprintf("suppress:%s:property:%d", s.providerID, propertyRID)
	return s.valkey.Exists(ctx, key)
}

// IsGeoSuppressed checks if a geographic region is suppressed for this provider.
func (s *SuppressionManager) IsGeoSuppressed(ctx context.Context, countryCode string) (bool, error) {
	key := fmt.Sprintf("suppress:%s:geo:%s", s.providerID, countryCode)
	return s.valkey.Exists(ctx, key)
}

// SuppressProperty adds a property suppression with a TTL.
func (s *SuppressionManager) SuppressProperty(ctx context.Context, propertyRID uint32, ttl time.Duration) error {
	key := fmt.Sprintf("suppress:%s:property:%d", s.providerID, propertyRID)
	return s.valkey.Set(ctx, key, "1", ttl)
}

// SuppressGeo adds a geo suppression with a TTL.
func (s *SuppressionManager) SuppressGeo(ctx context.Context, countryCode string, ttl time.Duration) error {
	key := fmt.Sprintf("suppress:%s:geo:%s", s.providerID, countryCode)
	return s.valkey.Set(ctx, key, "1", ttl)
}
