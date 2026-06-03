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
	configs          map[string]*targeting.PackageContextConfig
	activePackages   map[string][]string                   // sellerURL|propertyID|country → pkgIDs
	artifactTopics   map[topicstore.Taxonomy]map[string][]string
	packageTopics    map[topicstore.Taxonomy]map[string][]string
	urlBlocked       map[string]map[string]struct{} // packageID → hashSet
	urlAllowed       map[string]map[string]struct{}
	suppressedProps  map[string]struct{} // providerID|propertyRID
	suppressedGeos   map[string]struct{} // providerID|country
}

// NewInMemory returns an empty InMemory.
func NewInMemory() *InMemory {
	return &InMemory{
		configs:         make(map[string]*targeting.PackageContextConfig),
		activePackages:  make(map[string][]string),
		artifactTopics:  make(map[topicstore.Taxonomy]map[string][]string),
		packageTopics:   make(map[topicstore.Taxonomy]map[string][]string),
		urlBlocked:      make(map[string]map[string]struct{}),
		urlAllowed:      make(map[string]map[string]struct{}),
		suppressedProps: make(map[string]struct{}),
		suppressedGeos:  make(map[string]struct{}),
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
// return for the given seller / property / country tuple. The `now`
// argument the engine passes is ignored — the in-memory impl doesn't
// model date filtering; callers register the already-filtered package
// set for the scenario they want to exercise.
func (s *InMemory) WithActivePackages(sellerAgentURL, propertyID, country string, packageIDs []string) *InMemory {
	s.activePackages[activeKey(sellerAgentURL, propertyID, country)] = append([]string(nil), packageIDs...)
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

// WithURLBlocked adds a hash to a package's blocklist.
func (s *InMemory) WithURLBlocked(packageID, urlHash string) *InMemory {
	set := s.urlBlocked[packageID]
	if set == nil {
		set = make(map[string]struct{})
		s.urlBlocked[packageID] = set
	}
	set[urlHash] = struct{}{}
	return s
}

// WithURLAllowed adds a hash to a package's allowlist.
func (s *InMemory) WithURLAllowed(packageID, urlHash string) *InMemory {
	set := s.urlAllowed[packageID]
	if set == nil {
		set = make(map[string]struct{})
		s.urlAllowed[packageID] = set
	}
	set[urlHash] = struct{}{}
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

// --- ContextStorage ---

func (s *InMemory) ActivePackages(_ context.Context, sellerAgentURL, propertyID, country string, _ time.Time) ([]string, error) {
	return append([]string(nil), s.activePackages[activeKey(sellerAgentURL, propertyID, country)]...), nil
}

func (s *InMemory) ContextConfig(_ context.Context, packageID string) (*targeting.PackageContextConfig, bool, error) {
	cfg, ok := s.configs[packageID]
	return cfg, ok, nil
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

func (s *InMemory) URLBlocked(_ context.Context, packageID, urlHash string) (bool, error) {
	set, ok := s.urlBlocked[packageID]
	if !ok {
		return false, nil
	}
	_, blocked := set[urlHash]
	return blocked, nil
}

func (s *InMemory) URLAllowed(_ context.Context, packageID, urlHash string) (bool, error) {
	set, ok := s.urlAllowed[packageID]
	if !ok {
		return false, nil
	}
	_, allowed := set[urlHash]
	return allowed, nil
}

func (s *InMemory) IsPropertySuppressed(_ context.Context, providerID, propertyRID string) (bool, error) {
	_, ok := s.suppressedProps[providerID+"|"+propertyRID]
	return ok, nil
}

func (s *InMemory) IsGeoSuppressed(_ context.Context, providerID, country string) (bool, error) {
	_, ok := s.suppressedGeos[providerID+"|"+country]
	return ok, nil
}

func activeKey(seller, property, country string) string {
	return seller + "|" + property + "|" + country
}
