// Package mediabuystore owns the storage layout for the context engine's
// media-buy data. Writer pipelines (media-buy sync, governance) import
// Service to put / remove media buys without assembling Valkey keys
// themselves; the context agent reads through Reader, optionally
// wrapped by an LRU cache decorator.
//
// Storage keys:
//
//   - "mediabuy:seller:{seller_agent_url}" → SET of media_buy_id
//   - "mediabuy:{media_buy_id}"            → JSON MediaBuy
//
// The seller key segment is the canonicalized seller_agent_url; same
// byte-for-byte string match convention as targeting/identityconfig.
// Callers are responsible for canonicalization before invoking the
// service.
package mediabuystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// MediaBuy is the persisted record describing an active media buy.
type MediaBuy struct {
	MediaBuyID  string            `json:"media_buy_id"`
	SellerAgentURL string            `json:"seller_agent_url"`
	StartDate   string            `json:"start_date"`   // YYYY-MM-DD
	EndDate     string            `json:"end_date"`     // YYYY-MM-DD
	Countries   []string          `json:"countries"`    // empty = all countries
	PropertyIDs []string          `json:"property_ids"` // empty = all seller properties
	Packages    []MediaBuyPackage `json:"packages"`
}

// MediaBuyPackage names one of the packages within a media buy.
//
// PlacementIDs scopes the package to a subset of the publisher's
// adagents.json placement registry: at request time the package is
// eligible only when req.PlacementID is in this list. An empty
// (or nil) PlacementIDs is the permissive default — the package
// serves any placement on its media buy's properties. Same shape as
// Countries / PropertyIDs on MediaBuy: empty list means "all,"
// non-empty restricts.
type MediaBuyPackage struct {
	PackageID    string   `json:"package_id"`
	MediaBuyID   string   `json:"media_buy_id"`
	FormatIDs    []string `json:"format_ids,omitempty"`
	PlacementIDs []string `json:"placement_ids,omitempty"`
}

// Store is the minimal Valkey surface this package consumes. Production
// backends (redisstore, glidestore) satisfy it; tests use the in-memory
// Mock in this package.
type Store interface {
	SetMembers(ctx context.Context, key string) ([]string, error)
	MGet(ctx context.Context, keys ...string) ([]string, error)
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	SetAdd(ctx context.Context, key string, members ...string) error
	SetRemove(ctx context.Context, key string, members ...string) error
	Del(ctx context.Context, keys ...string) error
}

// SellerSetKey returns the key holding the set of media_buy_id values
// for a seller. Exported so writers / readers that bypass the Service
// (e.g., one-off migration scripts) share the key shape.
func SellerSetKey(sellerAgentURL string) string {
	return "mediabuy:seller:" + sellerAgentURL
}

// MediaBuyKey returns the key holding the JSON payload for one media
// buy.
func MediaBuyKey(mediaBuyID string) string {
	return "mediabuy:" + mediaBuyID
}

// Service is the write surface for media-buy data. Importers (media-buy
// sync, governance pipelines) put MediaBuy values without thinking
// about Valkey key construction.
type Service struct {
	store Store
}

// NewService constructs a Service backed by store. A nil store is an
// error.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("mediabuystore: store is required")
	}
	return &Service{store: store}, nil
}

// Put writes (or replaces) one media buy. The media-buy ID is added to
// the seller's set and the JSON payload is stored at the buy's key.
// SellerAgentURL, MediaBuyID, and (canonicalized) seller_agent_url MUST be
// non-empty; everything else is preserved verbatim.
func (s *Service) Put(ctx context.Context, mb MediaBuy) error {
	if mb.MediaBuyID == "" {
		return errors.New("mediabuystore: media_buy_id is required")
	}
	if mb.SellerAgentURL == "" {
		return errors.New("mediabuystore: seller_agent_url is required")
	}
	payload, err := json.Marshal(mb)
	if err != nil {
		return fmt.Errorf("mediabuystore: marshal: %w", err)
	}
	if err := s.store.Set(ctx, MediaBuyKey(mb.MediaBuyID), string(payload), 0); err != nil {
		return fmt.Errorf("mediabuystore: persist media buy %q: %w", mb.MediaBuyID, err)
	}
	if err := s.store.SetAdd(ctx, SellerSetKey(mb.SellerAgentURL), mb.MediaBuyID); err != nil {
		return fmt.Errorf("mediabuystore: index media buy %q under seller: %w", mb.MediaBuyID, err)
	}
	return nil
}

// Remove deletes one media buy. The seller's index is updated. Missing
// records are a no-op.
func (s *Service) Remove(ctx context.Context, sellerAgentURL, mediaBuyID string) error {
	if sellerAgentURL == "" {
		return errors.New("mediabuystore: seller_agent_url is required")
	}
	if mediaBuyID == "" {
		return errors.New("mediabuystore: media_buy_id is required")
	}
	if err := s.store.SetRemove(ctx, SellerSetKey(sellerAgentURL), mediaBuyID); err != nil {
		return fmt.Errorf("mediabuystore: deindex media buy %q: %w", mediaBuyID, err)
	}
	if err := s.store.Del(ctx, MediaBuyKey(mediaBuyID)); err != nil {
		return fmt.Errorf("mediabuystore: delete media buy %q: %w", mediaBuyID, err)
	}
	return nil
}

// Reader is the read surface the context agent consumes. The cache
// decorator (WithCache) implements the same interface as the direct
// reader.
type Reader interface {
	// MediaBuyIDsForSeller returns the set of media-buy IDs registered
	// under sellerAgentURL. Empty slice (with err == nil) means no
	// matching seller.
	MediaBuyIDsForSeller(ctx context.Context, sellerAgentURL string) ([]string, error)

	// MediaBuy returns the JSON-decoded record for one media buy.
	// ok == false (with err == nil) means no such media buy.
	MediaBuy(ctx context.Context, mediaBuyID string) (mb MediaBuy, ok bool, err error)

	// ActivePackages returns the packages from the seller's media buys
	// that are eligible at `now` for the given (propertyID, country,
	// placementID) tuple. A package is eligible iff:
	//
	//   - its media buy's date window contains `now`;
	//   - its media buy's Countries either is empty or includes country;
	//   - its media buy's PropertyIDs either is empty or includes propertyID;
	//   - the package's PlacementIDs either is empty or includes placementID.
	//
	// An empty placementID short-circuits the placement check — useful
	// for callers that have not yet plumbed placement_id through, but
	// production callers MUST pass req.PlacementID so cross-placement
	// leakage between different ad opportunities on the same property
	// cannot happen.
	ActivePackages(ctx context.Context, sellerAgentURL, propertyID, country, placementID string, now time.Time) ([]MediaBuyPackage, error)
}

// NewReader returns a direct Reader that issues one Valkey round-trip
// per call. Wrap with WithCache when reuse is expected.
func NewReader(store Store) Reader {
	return &reader{store: store}
}

type reader struct {
	store Store
}

func (r *reader) MediaBuyIDsForSeller(ctx context.Context, sellerAgentURL string) ([]string, error) {
	if sellerAgentURL == "" {
		return nil, errors.New("mediabuystore: seller_agent_url is required")
	}
	return r.store.SetMembers(ctx, SellerSetKey(sellerAgentURL))
}

func (r *reader) MediaBuy(ctx context.Context, mediaBuyID string) (MediaBuy, bool, error) {
	if mediaBuyID == "" {
		return MediaBuy{}, false, errors.New("mediabuystore: media_buy_id is required")
	}
	raw, ok, err := r.store.Get(ctx, MediaBuyKey(mediaBuyID))
	if err != nil {
		return MediaBuy{}, false, err
	}
	if !ok || raw == "" {
		return MediaBuy{}, false, nil
	}
	var mb MediaBuy
	if err := json.Unmarshal([]byte(raw), &mb); err != nil {
		return MediaBuy{}, false, fmt.Errorf("mediabuystore: decode media buy %q: %w", mediaBuyID, err)
	}
	return mb, true, nil
}

func (r *reader) ActivePackages(ctx context.Context, sellerAgentURL, propertyID, country, placementID string, now time.Time) ([]MediaBuyPackage, error) {
	ids, err := r.MediaBuyIDsForSeller(ctx, sellerAgentURL)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = MediaBuyKey(id)
	}
	values, err := r.store.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	var out []MediaBuyPackage
	for _, raw := range values {
		if raw == "" {
			continue
		}
		var mb MediaBuy
		if err := json.Unmarshal([]byte(raw), &mb); err != nil {
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
	return out, nil
}

func isActive(mb MediaBuy, now time.Time) bool {
	today := now.UTC().Truncate(24 * time.Hour)
	if mb.StartDate != "" {
		start, err := time.Parse("2006-01-02", mb.StartDate)
		if err != nil {
			return false
		}
		if today.Before(start) {
			return false
		}
	}
	if mb.EndDate != "" {
		end, err := time.Parse("2006-01-02", mb.EndDate)
		if err != nil {
			return false
		}
		if today.After(end) {
			return false
		}
	}
	return true
}

func matchesGeo(mb MediaBuy, country string) bool {
	if len(mb.Countries) == 0 {
		return true
	}
	return slices.Contains(mb.Countries, country)
}

func matchesProperty(mb MediaBuy, propertyID string) bool {
	if len(mb.PropertyIDs) == 0 {
		return true
	}
	return slices.Contains(mb.PropertyIDs, propertyID)
}

// matchesPlacement reports whether pkg is eligible to serve
// placementID. An empty placementID skips the check (compatibility for
// callers that have not yet plumbed PlacementID); empty PlacementIDs
// on the package means the package serves any placement.
func matchesPlacement(pkg MediaBuyPackage, placementID string) bool {
	if placementID == "" || len(pkg.PlacementIDs) == 0 {
		return true
	}
	return slices.Contains(pkg.PlacementIDs, placementID)
}
