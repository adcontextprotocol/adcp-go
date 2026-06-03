package topicstore

import (
	"context"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// CacheConfig sizes the two topic caches the wrapper maintains:
// artifact-side (topics:artifact:{src}:{id}:{ref}) and package-side
// (topics:package:{src}:{id}:{pkg}).
type CacheConfig struct {
	// ArtifactSize bounds the number of (taxonomy, artifact) entries
	// cached. Dominant cardinality: a high-traffic publisher has one
	// entry per URL per accepted taxonomy.
	ArtifactSize int
	// ArtifactTTL is the lifetime of one artifact-side cache entry.
	ArtifactTTL time.Duration

	// PackageSize bounds the number of (taxonomy, package) entries
	// cached. Bounded by #packages × #accepted-taxonomies.
	PackageSize int
	// PackageTTL is the lifetime of one package-side cache entry.
	PackageTTL time.Duration
}

// WithCache wraps an existing Reader (constructed via NewReader) in an
// LRU cache. Negative results are cached under the same TTL so a
// missing artifact / package key does not re-hit Valkey on every
// request.
func WithCache(r *Reader, cfg CacheConfig) *Reader {
	if r == nil {
		return nil
	}
	delegate := &cachedDelegate{
		inner:     r,
		artifacts: lru.NewLRU[string, cachedTopics](cfg.ArtifactSize, nil, cfg.ArtifactTTL),
		packages:  lru.NewLRU[string, cachedTopics](cfg.PackageSize, nil, cfg.PackageTTL),
	}
	return &Reader{store: delegate}
}

type cachedTopics struct {
	Topics  []string
	Present bool
}

// cachedDelegate is the ReaderStore the cached Reader is wired to. It
// intercepts SetMembers (the only ReaderStore method Reader uses today),
// passes SetIntersect through to the inner reader's store, and avoids
// caching SetIntersect since its keys vary per request.
type cachedDelegate struct {
	inner     *Reader
	artifacts *lru.LRU[string, cachedTopics]
	packages  *lru.LRU[string, cachedTopics]
}

// ArtifactKeyPrefix and PackageKeyPrefix are the literal segments
// ArtifactKey / PackageKey produce before the (source, id, ref/pkg)
// suffix. Exported so the cache decorator's key-shape branching shares
// the same source of truth as the key constructors.
const (
	ArtifactKeyPrefix = "topics:artifact:"
	PackageKeyPrefix  = "topics:package:"
)

func (d *cachedDelegate) SetMembers(ctx context.Context, key string) ([]string, error) {
	switch {
	case strings.HasPrefix(key, ArtifactKeyPrefix):
		return d.cachedSetMembers(ctx, key, d.artifacts)
	case strings.HasPrefix(key, PackageKeyPrefix):
		return d.cachedSetMembers(ctx, key, d.packages)
	default:
		return d.inner.store.SetMembers(ctx, key)
	}
}

func (d *cachedDelegate) cachedSetMembers(ctx context.Context, key string, cache *lru.LRU[string, cachedTopics]) ([]string, error) {
	if hit, ok := cache.Get(key); ok {
		if !hit.Present {
			return nil, nil
		}
		out := make([]string, len(hit.Topics))
		copy(out, hit.Topics)
		return out, nil
	}
	got, err := d.inner.store.SetMembers(ctx, key)
	if err != nil {
		return nil, err
	}
	cp := make([]string, len(got))
	copy(cp, got)
	cache.Add(key, cachedTopics{Topics: cp, Present: len(got) > 0})
	return got, nil
}

func (d *cachedDelegate) SetIntersect(ctx context.Context, keys ...string) ([]string, error) {
	return d.inner.store.SetIntersect(ctx, keys...)
}
