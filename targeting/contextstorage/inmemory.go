// Package contextstorage provides an in-memory targeting.ContextStorage
// implementation suitable for engine unit tests, the reference
// context-agent, and any embedder that wants to build a ContextStorage
// by hand without standing up a real backing store.
//
// All methods are safe for concurrent reads after construction. Writes
// happen at setup time via the With* builders; mutating an InMemory
// from multiple goroutines after the engine has started serving
// requests is not supported.
package contextstorage

import (
	"context"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
)

// InMemory satisfies targeting.ContextStorage from plain Go maps.
// Construct via NewInMemory and populate with the With* methods.
type InMemory struct {
	configs         map[string]*targeting.PackageContextConfig
	activePackages  map[string][]string // sellerURL|propertyID|country → pkgIDs
	artifactTopics  map[topicstore.Taxonomy]map[string][]string
	packageTopics   map[topicstore.Taxonomy]map[string][]string
	suppressedProps map[string]struct{} // providerID|propertyRID
	suppressedGeos  map[string]struct{} // providerID|country
	signals         map[string]string   // signal:* key → CSV of signal IDs
}

// NewInMemory returns an empty InMemory.
func NewInMemory() *InMemory {
	return &InMemory{
		configs:         make(map[string]*targeting.PackageContextConfig),
		activePackages:  make(map[string][]string),
		artifactTopics:  make(map[topicstore.Taxonomy]map[string][]string),
		packageTopics:   make(map[topicstore.Taxonomy]map[string][]string),
		suppressedProps: make(map[string]struct{}),
		suppressedGeos:  make(map[string]struct{}),
		signals:         make(map[string]string),
	}
}

// WithPackage registers a package's context config. Chainable.
func (s *InMemory) WithPackage(cfg *targeting.PackageContextConfig) *InMemory {
	if cfg != nil && cfg.PackageID != "" {
		s.configs[cfg.PackageID] = cfg
	}
	return s
}

// WithActivePackages registers the package IDs ActivePackages should
// return for the given (seller, property, country, placement) tuple.
// The `now` argument the engine passes is ignored — the in-memory
// impl doesn't model date filtering; callers register the
// already-filtered package set for the scenario they want to exercise.
//
// An empty placementID matches any inbound placement (engine tests
// that don't care about placement scoping can leave it blank).
func (s *InMemory) WithActivePackages(sellerAgentURL, propertyID, country, placementID string, packageIDs []string) *InMemory {
	s.activePackages[activeKey(sellerAgentURL, propertyID, country, placementID)] = append([]string(nil), packageIDs...)
	return s
}

// WithArtifactTopics registers the topic ids attached to an artifact
// ref under a taxonomy.
func (s *InMemory) WithArtifactTopics(tax topicstore.Taxonomy, ref string, topics []string) *InMemory {
	byRef := s.artifactTopics[tax]
	if byRef == nil {
		byRef = make(map[string][]string)
		s.artifactTopics[tax] = byRef
	}
	byRef[ref] = append([]string(nil), topics...)
	return s
}

// WithPackageTopics registers the topic ids a package targets under a
// taxonomy.
func (s *InMemory) WithPackageTopics(tax topicstore.Taxonomy, packageID string, topics []string) *InMemory {
	byPkg := s.packageTopics[tax]
	if byPkg == nil {
		byPkg = make(map[string][]string)
		s.packageTopics[tax] = byPkg
	}
	byPkg[packageID] = append([]string(nil), topics...)
	return s
}

// WithSuppressedProperty marks (providerID, propertyRID) as suppressed.
func (s *InMemory) WithSuppressedProperty(providerID, propertyRID string) *InMemory {
	s.suppressedProps[providerID+"|"+propertyRID] = struct{}{}
	return s
}

// WithSuppressedGeo marks (providerID, country) as suppressed.
func (s *InMemory) WithSuppressedGeo(providerID, country string) *InMemory {
	s.suppressedGeos[providerID+"|"+country] = struct{}{}
	return s
}

// WithSignalValue seeds one signal:* key with its CSV-encoded signal-id
// payload. Use signalstore.Key to build the key argument so the bytes
// match what the engine queries at request time.
func (s *InMemory) WithSignalValue(key, csvSignalIDs string) *InMemory {
	s.signals[key] = csvSignalIDs
	return s
}

// --- ContextStorage ---

// ActivePackages returns the package IDs registered for the tuple via
// WithActivePackages. When no explicit registration exists for the
// tuple, it falls back to every package supplied via WithPackage —
// the test-friendly default for the many tests that don't care about
// active-set scoping but still need the engine's active-set lookup
// to find their package.
func (s *InMemory) ActivePackages(_ context.Context, sellerAgentURL, propertyID, country, placementID string, _ time.Time) ([]string, error) {
	if ids, ok := s.activePackages[activeKey(sellerAgentURL, propertyID, country, placementID)]; ok {
		return append([]string(nil), ids...), nil
	}
	out := make([]string, 0, len(s.configs))
	for id := range s.configs {
		out = append(out, id)
	}
	return out, nil
}

func (s *InMemory) ContextConfig(_ context.Context, packageID string) (*targeting.PackageContextConfig, bool, error) {
	cfg, ok := s.configs[packageID]
	return cfg, ok, nil
}

func (s *InMemory) ContextConfigs(_ context.Context, packageIDs []string) ([]*targeting.PackageContextConfig, error) {
	out := make([]*targeting.PackageContextConfig, len(packageIDs))
	for i, id := range packageIDs {
		out[i] = s.configs[id]
	}
	return out, nil
}

func (s *InMemory) ArtifactTopics(_ context.Context, tax topicstore.Taxonomy, ref string) ([]string, error) {
	byRef, ok := s.artifactTopics[tax]
	if !ok {
		return nil, nil
	}
	return append([]string(nil), byRef[ref]...), nil
}

func (s *InMemory) PackageTopics(_ context.Context, tax topicstore.Taxonomy, packageID string) ([]string, error) {
	byPkg, ok := s.packageTopics[tax]
	if !ok {
		return nil, nil
	}
	return append([]string(nil), byPkg[packageID]...), nil
}

func (s *InMemory) IsPropertySuppressed(_ context.Context, providerID, propertyRID string) (bool, error) {
	_, ok := s.suppressedProps[providerID+"|"+propertyRID]
	return ok, nil
}

func (s *InMemory) IsGeoSuppressed(_ context.Context, providerID, country string) (bool, error) {
	_, ok := s.suppressedGeos[providerID+"|"+country]
	return ok, nil
}

func (s *InMemory) SignalMGet(_ context.Context, keys ...string) ([]string, error) {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = s.signals[k]
	}
	return out, nil
}

func activeKey(seller, property, country, placement string) string {
	return seller + "|" + property + "|" + country + "|" + placement
}
