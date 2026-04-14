package router

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

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// SignatureCache caches Ed25519 signatures per (placement_id, package_set_hash).
// Since available_packages is stable per placement (it doesn't change per user),
// the signature for a given placement config can be computed once and reused.
// This reduces signing cost from ~14μs to ~57ns (cache lookup).
type SignatureCache struct {
	mu               sync.RWMutex
	cache            map[string]sigEntry
	byEpoch          map[int64][]string  // epoch → cache keys for O(k) eviction
	byPlacement      map[string][]string // placementID → cache keys for O(k) Invalidate
	byKey            map[string]int64    // cache key → epoch for O(1) byEpoch cleanup
	byKeyPlacement   map[string]string   // cache key → placementID for O(1) byPlacement cleanup
	privKey          ed25519.PrivateKey
	maxSize          int // 0 = unlimited
	hits             atomic.Int64
	misses           atomic.Int64
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
		cache:          make(map[string]sigEntry),
		byEpoch:        make(map[int64][]string),
		byPlacement:    make(map[string][]string),
		byKey:          make(map[string]int64),
		byKeyPlacement: make(map[string]string),
		privKey:        privKey,
		maxSize:        maxSize,
	}
}

// SignOrCache returns a cached base64-encoded signature if available, otherwise signs and caches.
func (sc *SignatureCache) SignOrCache(req *tmproto.ContextMatchRequest) string {
	// Compute epoch once and use it for both the cache key and the signature payload
	// to prevent a key/signature epoch mismatch at day boundaries.
	epoch := tmproto.CurrentEpoch()
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

	// Sign with the epoch already captured above.
	payload := tmproto.CanonicalizeForSigning(req, epoch)
	sig := ed25519.Sign(sc.privKey, payload)
	b64Sig := base64.RawURLEncoding.EncodeToString(sig)

	sc.mu.Lock()
	// Double-check: another goroutine may have signed and cached while we signed.
	if entry, ok := sc.cache[key]; ok {
		sc.mu.Unlock()
		return entry.sig
	}
	sc.cache[key] = sigEntry{sig: b64Sig, epoch: epoch}
	sc.byEpoch[epoch] = append(sc.byEpoch[epoch], key)
	sc.byPlacement[req.PlacementID] = append(sc.byPlacement[req.PlacementID], key)
	sc.byKey[key] = epoch
	sc.byKeyPlacement[key] = req.PlacementID
	sc.evictLocked(epoch)
	sc.mu.Unlock()

	return b64Sig
}

// evictLocked removes old-epoch entries when cache exceeds maxSize.
// Uses the byEpoch secondary index so eviction is O(old_keys) not O(n).
// Must be called with sc.mu held for writing.
func (sc *SignatureCache) evictLocked(currentEpoch int64) {
	if sc.maxSize <= 0 || len(sc.cache) <= sc.maxSize {
		return
	}
	// Remove all entries from previous epochs first.
	for epoch, keys := range sc.byEpoch {
		if epoch >= currentEpoch {
			continue
		}
		for _, k := range keys {
			sc.deleteKeyLocked(k)
		}
		delete(sc.byEpoch, epoch)
	}
	// If still over capacity, remove the oldest remaining epoch's entries.
	for len(sc.cache) > sc.maxSize {
		var oldest int64 = -1
		for epoch := range sc.byEpoch {
			if oldest == -1 || epoch < oldest {
				oldest = epoch
			}
		}
		if oldest == -1 {
			break
		}
		for _, k := range sc.byEpoch[oldest] {
			sc.deleteKeyLocked(k)
		}
		delete(sc.byEpoch, oldest)
	}
}

// deleteKeyLocked removes a single cache key from cache, byKey, and byPlacement.
// byEpoch cleanup is the caller's responsibility (done in bulk per epoch bucket).
// Must be called with sc.mu held for writing.
func (sc *SignatureCache) deleteKeyLocked(k string) {
	delete(sc.cache, k)
	delete(sc.byKey, k)
	if placement, ok := sc.byKeyPlacement[k]; ok {
		sc.byPlacement[placement] = removeString(sc.byPlacement[placement], k)
		if len(sc.byPlacement[placement]) == 0 {
			delete(sc.byPlacement, placement)
		}
		delete(sc.byKeyPlacement, k)
	}
}

// removeString returns s with the first occurrence of v removed.
func removeString(s []string, v string) []string {
	for i, item := range s {
		if item == v {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
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
// Uses the byPlacement secondary index so removal is O(k) not O(n).
func (sc *SignatureCache) Invalidate(placementID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, k := range sc.byPlacement[placementID] {
		if epoch, ok := sc.byKey[k]; ok {
			sc.byEpoch[epoch] = removeString(sc.byEpoch[epoch], k)
			if len(sc.byEpoch[epoch]) == 0 {
				delete(sc.byEpoch, epoch)
			}
		}
		delete(sc.cache, k)
		delete(sc.byKey, k)
		delete(sc.byKeyPlacement, k)
	}
	delete(sc.byPlacement, placementID)
}

// InvalidateAll clears the entire signature cache.
func (sc *SignatureCache) InvalidateAll() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.cache = make(map[string]sigEntry)
	sc.byEpoch = make(map[int64][]string)
	sc.byPlacement = make(map[string][]string)
	sc.byKey = make(map[string]int64)
	sc.byKeyPlacement = make(map[string]string)
}

// Len returns the number of cached signatures.
func (sc *SignatureCache) Len() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return len(sc.cache)
}

// VerifySignature verifies an Ed25519 signature on a context match request.
// Delegates to tmproto.VerifyRequestSignature.
func VerifySignature(req *tmproto.ContextMatchRequest, b64Sig string, pubKey ed25519.PublicKey) bool {
	return tmproto.VerifyRequestSignature(req, b64Sig, pubKey)
}

// cacheKey builds a deterministic key from placement_id + sorted package IDs.
func cacheKey(placementID string, packages []tmproto.AvailablePackage) string {
	ids := make([]string, len(packages))
	for i, p := range packages {
		ids[i] = p.PackageID
	}
	sort.Strings(ids)
	combined := placementID + ":" + strings.Join(ids, ",")
	h := sha256.Sum256([]byte(combined))
	return placementID + ":" + hex.EncodeToString(h[:8])
}
