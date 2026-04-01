package router

import (
	"crypto/ed25519"
	"crypto/sha256"
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
func (sc *SignatureCache) SignOrCache(req *tmproto.ContextMatchRequest) string {
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

	// Slow path: sign and cache
	b64Sig := tmproto.SignRequest(req, sc.privKey)

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
	for k, e := range sc.cache {
		if e.epoch < currentEpoch {
			delete(sc.cache, k)
		}
	}
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
