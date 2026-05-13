package targeting

import (
	"slices"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// ResolvedPackages is a pre-computed, cacheable snapshot of all package data
// for a seller+property+country combination. Built by the resolver, cached
// with a TTL, and used by the engine for index-driven evaluation.
type ResolvedPackages struct {
	Packages []tmproto.AvailablePackage

	// Context indexes (zero Store calls at eval time).
	PropertyIndex     map[string][]string            // propertyRID → packageIDs
	TopicIndex        map[string][]string            // topic → packageIDs
	URLBlocklistIndex map[string][]string            // urlHash → packageIDs that block it
	URLAllowlists     map[string]map[string]struct{} // pkgID → set of allowed urlHashes

	// Pre-loaded configs.
	ContextConfigs  map[string]*PackageContextConfig  // pkgID → config
	IdentityConfigs map[string]*PackageIdentityConfig // pkgID → config
}

// ContextCandidates returns package IDs that could match the given property.
// Returns nil if no PropertyIndex entry exists (all packages are candidates).
func (r *ResolvedPackages) ContextCandidates(propertyRID string) map[string]struct{} {
	pkgIDs, ok := r.PropertyIndex[propertyRID]
	if !ok {
		return nil
	}
	set := make(map[string]struct{}, len(pkgIDs))
	for _, id := range pkgIDs {
		set[id] = struct{}{}
	}
	return set
}

// TopicCandidates returns package IDs that match any of the given topics.
func (r *ResolvedPackages) TopicCandidates(topics []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, topic := range topics {
		for _, pkgID := range r.TopicIndex[topic] {
			result[pkgID] = struct{}{}
		}
	}
	return result
}

// IsURLBlocked checks if a URL hash is blocked for a given package.
func (r *ResolvedPackages) IsURLBlocked(pkgID, urlHash string) bool {
	return slices.Contains(r.URLBlocklistIndex[urlHash], pkgID)
}

// IsURLAllowed checks if a URL hash is in the package's allowlist.
// Returns true if the package has no allowlist (no restriction).
func (r *ResolvedPackages) IsURLAllowed(pkgID, urlHash string) bool {
	allowlist, hasAllowlist := r.URLAllowlists[pkgID]
	if !hasAllowlist {
		return true
	}
	_, allowed := allowlist[urlHash]
	return allowed
}
