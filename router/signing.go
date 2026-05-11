package router

import (
	"sort"
	"strings"
	"sync"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// contextSignatureCache memoizes context-match signatures by
// (placement_id, provider_endpoint_url, package_ids, epoch). The Ed25519
// signature is bound to the exact signing input, so the cache key MUST cover
// every field the signing input depends on. The spec mandates that
// package_ids is constant per placement, which would make caching by
// (placement_id, provider_endpoint_url, epoch) sufficient for spec-compliant
// traffic — but the publisher controls package_ids, so including it in the
// key turns a spec violation into a transparent cache miss instead of a
// signature/body mismatch the provider has to reject.
//
// The cache is bounded — when it exceeds maxEntries, eviction drops the oldest
// epoch's entries first, then resets. Reference deployments serve a small
// number of placements, so a simple cap with epoch-based eviction is sufficient.
type contextSignatureCache struct {
	mu         sync.Mutex
	entries    map[contextSignatureCacheKey]string
	maxEntries int
}

type contextSignatureCacheKey struct {
	placementID string
	endpointURL string
	packageIDs  string
	epoch       int64
}

// packageIDsKey serializes the package_ids slice into the same form the
// signing input uses: sorted, comma-joined. Two slices with the same elements
// in any order share a cache entry; differing elements get separate entries.
func packageIDsKey(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func newContextSignatureCache(maxEntries int) *contextSignatureCache {
	if maxEntries <= 0 {
		maxEntries = 10_000
	}
	return &contextSignatureCache{
		entries:    make(map[contextSignatureCacheKey]string),
		maxEntries: maxEntries,
	}
}

// signatureFor returns a cached signature for (placementID, endpointURL, epoch),
// computing one with signer if absent.
func (c *contextSignatureCache) signatureFor(
	signer *tmproto.Signer,
	req *tmproto.ContextMatchRequest,
	endpointURL string,
	epoch int64,
) string {
	key := contextSignatureCacheKey{
		placementID: req.PlacementID,
		endpointURL: endpointURL,
		packageIDs:  packageIDsKey(req.PackageIDs),
		epoch:       epoch,
	}
	c.mu.Lock()
	if sig, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return sig
	}
	c.mu.Unlock()

	sig := signer.SignContextMatch(req, endpointURL, epoch)

	c.mu.Lock()
	if len(c.entries) >= c.maxEntries {
		c.evictOldEpochsLocked(epoch)
	}
	c.entries[key] = sig
	c.mu.Unlock()
	return sig
}

// evictOldEpochsLocked drops every entry whose epoch is older than current-1.
// Caller must hold c.mu.
func (c *contextSignatureCache) evictOldEpochsLocked(currentEpoch int64) {
	for k := range c.entries {
		if k.epoch < currentEpoch-1 {
			delete(c.entries, k)
		}
	}
	if len(c.entries) < c.maxEntries {
		return
	}
	// Still over cap (lots of distinct placements/providers in one epoch) —
	// reset entirely. Better to re-sign than to attempt LRU bookkeeping that
	// doesn't pay for itself at our scale.
	c.entries = make(map[contextSignatureCacheKey]string, c.maxEntries)
}

// signContextHeaders returns the X-AdCP-Signature / X-AdCP-Key-Id headers for
// a context-match fan-out to providerEndpoint, or nil if signing is disabled.
func (r *Router) signContextHeaders(req *tmproto.ContextMatchRequest, providerEndpoint string) map[string]string {
	if r.signer == nil {
		return nil
	}
	endpoint := tmproto.NormalizeProviderEndpointURL(providerEndpoint)
	epoch := tmproto.CurrentEpoch()
	sig := r.contextSigs.signatureFor(r.signer, req, endpoint, epoch)
	return map[string]string{
		tmproto.HeaderTMPSignature: sig,
		tmproto.HeaderTMPKeyID:     r.signer.KeyID,
	}
}

// signIdentityHeaders returns the X-AdCP-Signature / X-AdCP-Key-Id headers for
// an identity-match fan-out to providerEndpoint. Identity signatures are not
// cacheable — each request_id produces a unique signing input — so this builds
// a fresh signature on every call.
func (r *Router) signIdentityHeaders(req *tmproto.IdentityMatchRequest, providerEndpoint string) (map[string]string, error) {
	if r.signer == nil {
		return nil, nil
	}
	endpoint := tmproto.NormalizeProviderEndpointURL(providerEndpoint)
	sig, err := r.signer.SignIdentityMatch(req, endpoint, tmproto.CurrentEpoch())
	if err != nil {
		return nil, err
	}
	return map[string]string{
		tmproto.HeaderTMPSignature: sig,
		tmproto.HeaderTMPKeyID:     r.signer.KeyID,
	}, nil
}
