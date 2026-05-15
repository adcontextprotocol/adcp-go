// Package glidestore provides Valkey-backed implementations of
// targeting.ContextStore, targeting/fcap.Store, and targeting/audience.Store
// using valkey-glide/go/v2.
//
// Two topologies are supported through a single Store type:
//
//   - Standalone: one Valkey endpoint. Construct via New with a
//     *glide.Client.
//   - Shadow shards: N independent standalone endpoints that mirror a
//     central cluster's per-shard keyspace. Construct via NewShadow
//     with one *glide.Client per shard ordinal. Reads route by
//     app-level CRC16; writes return ErrReadOnly.
//
// Cluster topology via *glide.ClusterClient is not currently wired
// here; the redisstore package supports it.
//
// See the redisstore package doc for routing details — both packages
// share the same slot/shard contract via targeting/internal/clusterslot.
package glidestore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	glide "github.com/valkey-io/valkey-glide/go/v2"
	"github.com/valkey-io/valkey-glide/go/v2/options"
	"github.com/valkey-io/valkey-glide/go/v2/pipeline"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/clusterslot"
)

var (
	_ targeting.ContextStore = (*Store)(nil)
	_ fcap.Store             = (*Store)(nil)
)

// ErrReadOnly is returned by every write method when Store is in
// shadow-shards mode.
var ErrReadOnly = errors.New("glidestore: write not supported on shadow replica")

// ErrCrossShard is returned when a multi-key operation receives keys
// that span shards. Co-locate keys with a `{hashtag}` to keep them on
// one shard.
var ErrCrossShard = errors.New("glidestore: cross-shard keys are not supported")

// Store implements targeting.ContextStore, fcap.Store, and audience.Store on
// top of valkey-glide.
type Store struct {
	client   *glide.Client
	shards   []*glide.Client
	shardMap *clusterslot.ShardMap
}

// New constructs a Store backed by a single standalone glide client.
func New(client *glide.Client) *Store {
	return &Store{client: client}
}

// NewShadow constructs a read-only Store that fans out reads across
// shards. shards[i] is the standalone client for shard ordinal i; index
// order is load-bearing for routing. Returns an error if shards is
// empty or any element is nil.
func NewShadow(shards []*glide.Client) (*Store, error) {
	if len(shards) == 0 {
		return nil, errors.New("glidestore: at least one shadow shard is required")
	}
	for i, c := range shards {
		if c == nil {
			return nil, fmt.Errorf("glidestore: shadow shard at ordinal %d is nil", i)
		}
	}
	return &Store{shards: shards, shardMap: clusterslot.NewShardMap(len(shards))}, nil
}

// NumShards reports the shard count for a shadow-shards Store. Returns
// 1 for the standalone topology.
func (s *Store) NumShards() int {
	if len(s.shards) > 0 {
		return len(s.shards)
	}
	return 1
}

func (s *Store) shadow() bool { return len(s.shards) > 0 }

func (s *Store) clientFor(key string) *glide.Client {
	if s.shadow() {
		return s.shards[s.shardMap.Shard(key)]
	}
	return s.client
}

func (s *Store) clientForGroup(group int) *glide.Client {
	if s.shadow() {
		return s.shards[group]
	}
	return s.client
}

func (s *Store) groupKeys(keys []string) map[int][]int {
	if !s.shadow() {
		all := make([]int, len(keys))
		for i := range keys {
			all[i] = i
		}
		return map[int][]int{0: all}
	}
	numShards := len(s.shards)
	out := make(map[int][]int, numShards)
	for i, k := range keys {
		sh := s.shardMap.Shard(k)
		out[sh] = append(out[sh], i)
	}
	return out
}

func (s *Store) fanOut(ctx context.Context, byGroup map[int][]int, fn func(ctx context.Context, group int, indices []int) error) error {
	if len(byGroup) == 0 {
		return nil
	}
	if len(byGroup) == 1 {
		// Single-group fast path: skip goroutine overhead. Extract the
		// sole entry up front rather than relying on a return inside a
		// for-range body, which would silently fall through if the body
		// is ever refactored to continue.
		var (
			group   int
			indices []int
		)
		for group, indices = range byGroup { //nolint:revive // single iteration: extracts the only entry
		}
		return fn(ctx, group, indices)
	}
	derived, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	wg.Add(len(byGroup))
	for g, idxs := range byGroup {
		g, idxs := g, idxs
		go func() {
			defer wg.Done()
			if err := fn(derived, g, idxs); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// --- targeting.ContextStore ---

func (s *Store) SetIsMember(ctx context.Context, key, member string) (bool, error) {
	return s.clientFor(key).SIsMember(ctx, key, member)
}

func (s *Store) SetIntersect(ctx context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var c *glide.Client
	if s.shadow() {
		shard := s.shardMap.Shard(keys[0])
		for _, k := range keys[1:] {
			if s.shardMap.Shard(k) != shard {
				return nil, ErrCrossShard
			}
		}
		c = s.shards[shard]
	} else {
		c = s.client
	}
	set, err := c.SInter(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	res, err := s.clientFor(key).Get(ctx, key)
	if err != nil {
		return "", false, err
	}
	if res.IsNil() {
		return "", false, nil
	}
	return res.Value(), true, nil
}

func (s *Store) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if s.shadow() {
		return ErrReadOnly
	}
	if ttl <= 0 {
		_, err := s.client.Set(ctx, key, value)
		return err
	}
	opts := *options.NewSetOptions().SetExpiry(options.NewExpiryIn(ttl))
	_, err := s.client.SetWithOptions(ctx, key, value, opts)
	return err
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.clientFor(key).Exists(ctx, []string{key})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) SetMembers(ctx context.Context, key string) ([]string, error) {
	set, err := s.clientFor(key).SMembers(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(set) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out, nil
}

func (s *Store) MGet(ctx context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]string, len(keys))
	byGroup := s.groupKeys(keys)
	err := s.fanOut(ctx, byGroup, func(ctx context.Context, group int, indices []int) error {
		groupKeys := make([]string, len(indices))
		for j, i := range indices {
			groupKeys[j] = keys[i]
		}
		results, err := s.clientForGroup(group).MGet(ctx, groupKeys)
		if err != nil {
			return err
		}
		if len(results) != len(groupKeys) {
			return fmt.Errorf("glidestore: MGET returned %d results for %d keys", len(results), len(groupKeys))
		}
		for j, r := range results {
			if r.IsNil() {
				continue
			}
			out[indices[j]] = r.Value()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) MSet(ctx context.Context, kvs map[string]string, ttl time.Duration) error {
	if len(kvs) == 0 {
		return nil
	}
	if s.shadow() {
		return ErrReadOnly
	}
	if ttl <= 0 {
		_, err := s.client.MSet(ctx, kvs)
		return err
	}
	batch := pipeline.NewStandaloneBatch(false)
	expiryOpts := *options.NewSetOptions().SetExpiry(options.NewExpiryIn(ttl))
	for k, v := range kvs {
		batch.SetWithOptions(k, v, expiryOpts)
	}
	_, err := s.client.Exec(ctx, *batch, true)
	return err
}

// --- fcap.Store ---

func (s *Store) SetFields(ctx context.Context, key string, fields map[string]string, expireAt time.Time) error {
	if s.shadow() {
		return ErrReadOnly
	}
	if len(fields) == 0 {
		return nil
	}
	opts := options.NewHSetExOptions().SetExpiry(options.NewExpiryAt(expireAt))
	_, err := s.client.HSetEx(ctx, key, fields, opts)
	return err
}

func (s *Store) SetFieldsBatch(ctx context.Context, batches []fcap.FieldsBatch) error {
	if s.shadow() {
		return ErrReadOnly
	}
	if len(batches) == 0 {
		return nil
	}
	batch := pipeline.NewStandaloneBatch(false)
	queued := 0
	for _, b := range batches {
		if len(b.Fields) == 0 {
			continue
		}
		opts := options.NewHSetExOptions().SetExpiry(options.NewExpiryAt(b.ExpireAt))
		batch.HSetEx(b.Key, b.Fields, opts)
		queued++
	}
	if queued == 0 {
		return nil
	}
	_, err := s.client.Exec(ctx, *batch, true)
	return err
}

func (s *Store) FieldExists(ctx context.Context, key, field string) (bool, error) {
	return s.clientFor(key).HExists(ctx, key, field)
}

func (s *Store) FieldExistsBatch(ctx context.Context, lookups []fcap.FieldLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	keys := make([]string, len(lookups))
	for i, l := range lookups {
		keys[i] = l.Key
	}
	out := make([]bool, len(lookups))
	byGroup := s.groupKeys(keys)
	err := s.fanOut(ctx, byGroup, func(ctx context.Context, group int, indices []int) error {
		batch := pipeline.NewStandaloneBatch(false)
		for _, i := range indices {
			batch.HExists(lookups[i].Key, lookups[i].Field)
		}
		results, err := s.clientForGroup(group).Exec(ctx, *batch, true)
		if err != nil {
			return err
		}
		if len(results) != len(indices) {
			return fmt.Errorf("glidestore: HEXISTS batch returned %d results, expected %d", len(results), len(indices))
		}
		for j, r := range results {
			b, ok := r.(bool)
			if !ok {
				return fmt.Errorf("glidestore: HEXISTS result %d: expected bool, got %T", indices[j], r)
			}
			out[indices[j]] = b
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
