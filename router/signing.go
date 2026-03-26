package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// currentEpoch returns the daily epoch (days since Unix epoch).
// Used for replay protection: signatures include the epoch, bounding
// replay to ~48 hours (current + previous epoch accepted by verifiers).
func currentEpoch() int64 {
	return time.Now().Unix() / 86400
}

// SignatureCache caches Ed25519 signatures per (placement_id, package_set_hash).
// Since available_packages is stable per placement (it doesn't change per user),
// the signature for a given placement config can be computed once and reused.
// This reduces signing cost from ~14μs to ~57ns (cache lookup).
type SignatureCache struct {
	mu      sync.RWMutex
	cache   map[string]sigEntry
	privKey ed25519.PrivateKey
	maxSize int // 0 = unlimited
	hits    atomic.Int64
	misses  atomic.Int64
}

type sigEntry struct {
	sig   string
	epoch int64
}

// NewSignatureCache creates a signature cache with the given private key.
// maxSize controls eviction: when the cache exceeds maxSize, entries from
// older epochs are evicted first. 0 means unlimited (not recommended for production).
func NewSignatureCache(privKey ed25519.PrivateKey, maxSize int) *SignatureCache {
	return &SignatureCache{
		cache:   make(map[string]sigEntry),
		privKey: privKey,
		maxSize: maxSize,
	}
}

// SignOrCache returns a cached base64-encoded signature if available, otherwise signs and caches.
func (sc *SignatureCache) SignOrCache(req *tmp.ContextMatchRequest) string {
	epoch := currentEpoch()
	key := cacheKey(req.PlacementID, req.AvailablePkgs) + fmt.Sprintf(":%d", epoch)

	// Fast path: read lock
	sc.mu.RLock()
	if entry, ok := sc.cache[key]; ok {
		sc.mu.RUnlock()
		sc.hits.Add(1)
		return entry.sig
	}
	sc.mu.RUnlock()

	sc.misses.Add(1)

	// Slow path: compute signature (~14μs), then cache as base64
	payload := canonicalizeForSigning(req, epoch)
	rawSig := ed25519.Sign(sc.privKey, payload)
	b64Sig := base64.RawURLEncoding.EncodeToString(rawSig)

	sc.mu.Lock()
	sc.cache[key] = sigEntry{sig: b64Sig, epoch: epoch}
	sc.evictLocked(epoch)
	sc.mu.Unlock()

	return b64Sig
}

// evictLocked removes old-epoch entries when cache exceeds maxSize.
// Must be called with sc.mu held for writing.
func (sc *SignatureCache) evictLocked(currentEpoch int64) {
	if sc.maxSize <= 0 || len(sc.cache) <= sc.maxSize {
		return
	}
	// First pass: remove entries from older epochs
	for k, e := range sc.cache {
		if e.epoch < currentEpoch {
			delete(sc.cache, k)
		}
	}
	// If still over limit, remove arbitrary entries
	for k := range sc.cache {
		if len(sc.cache) <= sc.maxSize {
			break
		}
		delete(sc.cache, k)
	}
}

// Stats returns cache statistics.
func (sc *SignatureCache) Stats() *SigCacheStats {
	sc.mu.RLock()
	size := len(sc.cache)
	sc.mu.RUnlock()
	return &SigCacheStats{
		Size:    size,
		MaxSize: sc.maxSize,
		Hits:    sc.hits.Load(),
		Misses:  sc.misses.Load(),
	}
}

// Invalidate removes cached signatures for a specific placement.
func (sc *SignatureCache) Invalidate(placementID string) {
	prefix := placementID + ":"
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for k := range sc.cache {
		if strings.HasPrefix(k, prefix) {
			delete(sc.cache, k)
		}
	}
}

// InvalidateAll clears the entire signature cache.
func (sc *SignatureCache) InvalidateAll() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.cache = make(map[string]sigEntry)
}

// Len returns the number of cached signatures.
func (sc *SignatureCache) Len() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return len(sc.cache)
}

// cacheKey builds a deterministic key from placement_id + sorted package IDs.
func cacheKey(placementID string, packages []tmp.AvailablePackage) string {
	ids := make([]string, len(packages))
	for i, p := range packages {
		ids[i] = p.PackageID
	}
	sort.Strings(ids)
	combined := placementID + ":" + strings.Join(ids, ",")
	h := sha256.Sum256([]byte(combined))
	return placementID + ":" + hex.EncodeToString(h[:8])
}

// canonicalizeForSigning creates a deterministic byte representation of the
// STATIC parts of the request plus a daily epoch for replay protection.
// Does NOT include request_id (changes per request).
// Covers: property_id, property_rid, property_type, placement_id, sorted package_ids, epoch.
func canonicalizeForSigning(req *tmp.ContextMatchRequest, epoch int64) []byte {
	ids := make([]string, len(req.AvailablePkgs))
	for i, p := range req.AvailablePkgs {
		ids[i] = p.PackageID
	}
	sort.Strings(ids)

	payload := fmt.Sprintf("%s|%d|%s|%s|%s|%d",
		req.PropertyID,
		req.PropertyRID,
		req.PropertyType,
		req.PlacementID,
		strings.Join(ids, ","),
		epoch,
	)
	return []byte(payload)
}

// VerifySignature verifies an Ed25519 signature on a context match request.
// Accepts current or previous epoch to handle day boundaries (~48h replay window).
func VerifySignature(req *tmp.ContextMatchRequest, b64Sig string, pubKey ed25519.PublicKey) bool {
	sig, err := base64.RawURLEncoding.DecodeString(b64Sig)
	if err != nil {
		return false
	}
	epoch := currentEpoch()
	// Try current epoch first
	if ed25519.Verify(pubKey, canonicalizeForSigning(req, epoch), sig) {
		return true
	}
	// Try previous epoch (handles day boundary)
	return ed25519.Verify(pubKey, canonicalizeForSigning(req, epoch-1), sig)
}
