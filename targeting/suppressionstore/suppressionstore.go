// Package suppressionstore owns the storage layout for the context
// engine's per-provider property and geo suppression kill-switches.
//
// Storage keys:
//
//   - "suppress:{provider_id}:property:{property_rid}" → string (presence-only) with TTL
//   - "suppress:{provider_id}:geo:{country}"           → string (presence-only) with TTL
//
// The agent loads both keysets into an in-memory snapshot on startup
// and refreshes periodically. The Snapshot type holds the
// atomic-pointer-swapped state used at request time so suppression
// checks never block on a Valkey round-trip. The Service surface is
// used by operator tooling and any future suppression-writer pipeline
// to set / clear suppressions; both write paths take an explicit TTL
// because suppressions MUST self-expire.
package suppressionstore

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Store is the minimal Valkey surface this package consumes.
type Store interface {
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	Scan(ctx context.Context, match string) ([]string, error)
}

// PropertyKey returns the Valkey key for a property suppression.
func PropertyKey(providerID, propertyRID string) string {
	return "suppress:" + providerID + ":property:" + propertyRID
}

// GeoKey returns the Valkey key for a geo suppression.
func GeoKey(providerID, country string) string {
	return "suppress:" + providerID + ":geo:" + country
}

// propertyKeyPrefix returns the literal key segment that prefixes every
// property suppression for providerID. SCAN patterns are built by
// appending '*' to this; LoadAll strips it from each returned key to
// recover the bare property_rid.
func propertyKeyPrefix(providerID string) string {
	return "suppress:" + providerID + ":property:"
}

// geoKeyPrefix is the geo-side equivalent of propertyKeyPrefix.
func geoKeyPrefix(providerID string) string {
	return "suppress:" + providerID + ":geo:"
}

// PropertyPrefix returns the SCAN pattern matching every property
// suppression for a provider.
func PropertyPrefix(providerID string) string {
	return propertyKeyPrefix(providerID) + "*"
}

// GeoPrefix returns the SCAN pattern matching every geo suppression for
// a provider.
func GeoPrefix(providerID string) string {
	return geoKeyPrefix(providerID) + "*"
}

// MaxSuppressionTTL caps the duration a single suppression call can
// install. A multi-decade TTL on a kill switch is a footgun — there
// is no proactive cleanup of suppression keys outside the operator's
// own discipline, so a typo on the TTL would lock out a property /
// country until someone notices. Operators that genuinely need
// longer suppressions should re-issue or escalate to a registry-level
// change.
const MaxSuppressionTTL = 30 * 24 * time.Hour

// Service is the write surface for suppressions. Both methods require
// a positive TTL — a suppression with no expiry is operationally
// dangerous (it survives forever on accidental writes).
type Service struct {
	store Store
}

// NewService constructs a Service backed by store.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("suppressionstore: store is required")
	}
	return &Service{store: store}, nil
}

// SuppressProperty installs a property suppression for the duration.
// ttl MUST be positive and at most MaxSuppressionTTL.
func (s *Service) SuppressProperty(ctx context.Context, providerID, propertyRID string, ttl time.Duration) error {
	if providerID == "" {
		return errors.New("suppressionstore: provider_id is required")
	}
	if propertyRID == "" {
		return errors.New("suppressionstore: property_rid is required")
	}
	if ttl <= 0 {
		return errors.New("suppressionstore: ttl must be positive")
	}
	if ttl > MaxSuppressionTTL {
		return fmt.Errorf("suppressionstore: ttl %v exceeds MaxSuppressionTTL %v", ttl, MaxSuppressionTTL)
	}
	return s.store.Set(ctx, PropertyKey(providerID, propertyRID), "1", ttl)
}

// SuppressGeo installs a geo suppression for the duration. ttl MUST
// be positive and at most MaxSuppressionTTL.
func (s *Service) SuppressGeo(ctx context.Context, providerID, country string, ttl time.Duration) error {
	if providerID == "" {
		return errors.New("suppressionstore: provider_id is required")
	}
	if country == "" {
		return errors.New("suppressionstore: country is required")
	}
	if ttl <= 0 {
		return errors.New("suppressionstore: ttl must be positive")
	}
	if ttl > MaxSuppressionTTL {
		return fmt.Errorf("suppressionstore: ttl %v exceeds MaxSuppressionTTL %v", ttl, MaxSuppressionTTL)
	}
	return s.store.Set(ctx, GeoKey(providerID, country), "1", ttl)
}

// UnsuppressProperty drops a property suppression early. Missing keys
// are a no-op.
func (s *Service) UnsuppressProperty(ctx context.Context, providerID, propertyRID string) error {
	if providerID == "" {
		return errors.New("suppressionstore: provider_id is required")
	}
	if propertyRID == "" {
		return errors.New("suppressionstore: property_rid is required")
	}
	return s.store.Del(ctx, PropertyKey(providerID, propertyRID))
}

// UnsuppressGeo drops a geo suppression early.
func (s *Service) UnsuppressGeo(ctx context.Context, providerID, country string) error {
	if providerID == "" {
		return errors.New("suppressionstore: provider_id is required")
	}
	if country == "" {
		return errors.New("suppressionstore: country is required")
	}
	return s.store.Del(ctx, GeoKey(providerID, country))
}

// LoadAll scans every suppression key for providerID and returns the
// suppressed property RIDs and country codes. Used at startup and on
// each refresh tick of the in-memory Snapshot.
func LoadAll(ctx context.Context, store Store, providerID string) (properties, geos []string, err error) {
	if providerID == "" {
		return nil, nil, errors.New("suppressionstore: provider_id is required")
	}
	propertyKeys, err := store.Scan(ctx, PropertyPrefix(providerID))
	if err != nil {
		return nil, nil, fmt.Errorf("suppressionstore: scan property keys: %w", err)
	}
	geoKeys, err := store.Scan(ctx, GeoPrefix(providerID))
	if err != nil {
		return nil, nil, fmt.Errorf("suppressionstore: scan geo keys: %w", err)
	}
	propertyPrefix := propertyKeyPrefix(providerID)
	geoPrefix := geoKeyPrefix(providerID)

	properties = make([]string, 0, len(propertyKeys))
	for _, k := range propertyKeys {
		if len(k) > len(propertyPrefix) {
			properties = append(properties, k[len(propertyPrefix):])
		}
	}
	geos = make([]string, 0, len(geoKeys))
	for _, k := range geoKeys {
		if len(k) > len(geoPrefix) {
			geos = append(geos, k[len(geoPrefix):])
		}
	}
	return properties, geos, nil
}
