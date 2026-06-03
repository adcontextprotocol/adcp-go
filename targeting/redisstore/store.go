// Package redisstore provides Valkey-backed implementations of
// targeting.ContextStore, targeting/fcap.Store, and targeting/audience.Store
// using github.com/redis/go-redis/v9.
//
// Three topologies are supported through a single Store type:
//
//   - Standalone: a single Valkey endpoint. Construct via New with a
//     *redis.Client.
//   - Cluster: a Valkey Cluster. Construct via New with a
//     *redis.ClusterClient. Slot routing happens inside go-redis.
//   - Shadow shards: N independent standalone Valkey endpoints that
//     mirror a central cluster's per-shard keyspace and do not
//     participate in cluster gossip. Construct via NewShadow with one
//     *redis.Client per shard ordinal. Reads route by app-level CRC16;
//     writes return ErrReadOnly.
//
// One Store satisfies targeting.ContextStore, fcap.Store, and audience.Store
// in every topology, so callers share connection state between the
// targeting engine and the frequency-cap / audience services.
//
// # Shadow-shard routing
//
// shadowstore mode computes slot = CRC16(hashtag(key)) % 16384 and
// derives the shard ordinal as floor(slot / (16384 / N)), where N is
// the number of configured shadows; the last shard absorbs the
// remainder. This matches Valkey Cluster's positional
// `valkey-cli --cluster create` allocation, so shadow ordinal K serves
// the same slot range as cluster master K.
//
// Single-shard shadow deployments are valid: with N=1 every key routes
// to the single endpoint. Changing N (a resharding event on the
// upstream cluster) is a breaking change for in-flight reads — the
// slot-to-ordinal mapping shifts and a fraction of keys land on the
// wrong shadow. A deployment that straddles a migration must change
// this package to fall back to a second shadow on miss while the slot
// allocation converges.
//
// # Wiring from a Terraform-shaped shard map
//
// The Terraform configmap exposes shadow endpoints as a JSON map
// keyed by shard ordinal: `VALKEY_SHARDS = '{"0":"host:port","1":...}'`.
// Build the *redis.Client slice in numeric ordinal order:
//
//	var addrs map[string]string
//	_ = json.Unmarshal([]byte(os.Getenv("VALKEY_SHARDS")), &addrs)
//
//	ordinals := make([]int, 0, len(addrs))
//	for k := range addrs {
//	    i, err := strconv.Atoi(k)
//	    if err != nil { return err }
//	    ordinals = append(ordinals, i)
//	}
//	sort.Ints(ordinals)
//
//	clients := make([]*redis.Client, len(ordinals))
//	for i, ord := range ordinals {
//	    if ord != i {
//	        return fmt.Errorf("non-contiguous shard ordinals: %v", ordinals)
//	    }
//	    clients[i] = redis.NewClient(&redis.Options{
//	        Addr:     addrs[strconv.Itoa(ord)],
//	        Password: os.Getenv("VALKEY_PASSWORD"),
//	        DB:       db,
//	    })
//	}
//	store, err := redisstore.NewShadow(clients)
//
// Index order in the slice is load-bearing for routing. Caller closes
// each client at shutdown.
package redisstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/clusterslot"
)

var (
	_ fcap.Store = (*Store)(nil)
)

// ErrReadOnly is returned by every write method when Store is in
// shadow-shards mode. Shadow replicas are receive-only targets; writes
// must go to a cluster master through a cluster-aware client.
var ErrReadOnly = errors.New("redisstore: write not supported on shadow replica")

// ErrCrossShard is returned when a multi-key operation receives keys
// that span shards (shadow mode) or slots (cluster mode). The two
// affected callers are SetIntersect (Valkey SINTER requires same-slot
// keys) and any future multi-key command with the same constraint.
// Callers must co-locate related keys with a `{hashtag}` to keep them
// on one shard.
var ErrCrossShard = errors.New("redisstore: cross-shard keys are not supported")

// Store implements targeting.ContextStore, fcap.Store, and audience.Store on
// top of go-redis.
//
// In single-client mode (standalone or cluster) client is set and
// shards is nil. In shadow-shards mode shards is set, client is nil,
// shardMap precomputes slot boundaries, and writes return ErrReadOnly.
type Store struct {
	client   redis.UniversalClient
	shards   []*redis.Client
	shardMap *clusterslot.ShardMap
}

// New constructs a Store backed by a single go-redis client. Pass
// *redis.Client for standalone or *redis.ClusterClient for cluster
// topology — go-redis routes commands inside the client.
func New(client redis.UniversalClient) *Store {
	return &Store{client: client}
}

// NewShadow constructs a read-only Store that fans out reads across
// shards. shards[i] is the standalone client for shard ordinal i; index
// order is load-bearing for routing. Returns an error if shards is
// empty or any element is nil.
func NewShadow(shards []*redis.Client) (*Store, error) {
	if len(shards) == 0 {
		return nil, errors.New("redisstore: at least one shadow shard is required")
	}
	for i, c := range shards {
		if c == nil {
			return nil, fmt.Errorf("redisstore: shadow shard at ordinal %d is nil", i)
		}
	}
	return &Store{shards: shards, shardMap: clusterslot.NewShardMap(len(shards))}, nil
}

// NumShards reports the shard count for a shadow-shards Store. Returns
// 1 for single-client topologies.
func (s *Store) NumShards() int {
	if len(s.shards) > 0 {
		return len(s.shards)
	}
	return 1
}

// shadow reports whether Store is in shadow-shards mode.
func (s *Store) shadow() bool { return len(s.shards) > 0 }

// cmdableFor returns the go-redis Cmdable that serves single-key
// commands on key. In single-client mode this is the wrapped client; in
// shadow mode it is the per-shard client.
func (s *Store) cmdableFor(key string) redis.Cmdable {
	if s.shadow() {
		return s.shards[s.shardMap.Shard(key)]
	}
	return s.client
}

// pipelineFor returns a Pipeliner for the given destination group. In
// single-client mode the group identifier is ignored; in shadow mode
// the group is the shard ordinal.
func (s *Store) pipelineFor(group int) redis.Pipeliner {
	if s.shadow() {
		return s.shards[group].Pipeline()
	}
	return s.client.Pipeline()
}

// groupKeys maps each input index to its destination group. In
// single-client mode every index falls into group 0; in shadow mode
// indices are bucketed by shard ordinal.
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

// fanOut runs fn once per destination group. If only one group exists
// (single-client mode, or a shadow batch that lands on one shard), fn
// runs inline. With multiple groups fn runs per goroutine so a
// multi-shard batch costs one shard's round-trip of wall-clock
// latency. The first error from any shard cancels the derived context
// so in-flight peers bail rather than waiting out their own RTTs.
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
	return s.cmdableFor(key).SIsMember(ctx, key, member).Result()
}

// SetIntersect computes SINTER over keys. All keys must land on the
// same shard (in shadow mode) or the same slot (in cluster mode); the
// caller co-locates them via `{hashtag}`. Returns ErrCrossShard
// uniformly across topologies when that constraint is violated.
func (s *Store) SetIntersect(ctx context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if s.shadow() {
		shard := s.shardMap.Shard(keys[0])
		for _, k := range keys[1:] {
			if s.shardMap.Shard(k) != shard {
				return nil, ErrCrossShard
			}
		}
		res, err := s.shards[shard].SInter(ctx, keys...).Result()
		return res, mapCrossSlotErr(err)
	}
	res, err := s.client.SInter(ctx, keys...).Result()
	return res, mapCrossSlotErr(err)
}

// mapCrossSlotErr surfaces Valkey/Redis Cluster CROSSSLOT errors as
// ErrCrossShard so callers see the same sentinel across topologies.
func mapCrossSlotErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "CROSSSLOT") || strings.Contains(msg, "keys don't hash to the same slot") {
		return ErrCrossShard
	}
	return err
}

func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := s.cmdableFor(key).Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (s *Store) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if s.shadow() {
		return ErrReadOnly
	}
	return s.client.Set(ctx, key, value, ttl).Err()
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.cmdableFor(key).Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) SetMembers(ctx context.Context, key string) ([]string, error) {
	res, err := s.cmdableFor(key).SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, nil
	}
	return res, nil
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
		var (
			results []any
			err     error
		)
		if s.shadow() {
			results, err = s.shards[group].MGet(ctx, groupKeys...).Result()
		} else {
			results, err = s.client.MGet(ctx, groupKeys...).Result()
		}
		if err != nil {
			return err
		}
		if len(results) != len(groupKeys) {
			return fmt.Errorf("redisstore: MGET returned %d results for %d keys", len(results), len(groupKeys))
		}
		for j, r := range results {
			if r == nil {
				continue
			}
			if str, ok := r.(string); ok {
				out[indices[j]] = str
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetAdd executes SADD for the given key+members. Returns ErrReadOnly when
// the store is a shadow replica. Empty member lists are a no-op.
func (s *Store) SetAdd(ctx context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	if s.shadow() {
		return ErrReadOnly
	}
	args := make([]any, len(members))
	for i, m := range members {
		args[i] = m
	}
	return s.client.SAdd(ctx, key, args...).Err()
}

// SetRemove executes SREM for the given key+members. Returns ErrReadOnly
// when the store is a shadow replica. Empty member lists are a no-op.
// Missing keys and members are silently treated as no-ops by Valkey.
func (s *Store) SetRemove(ctx context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	if s.shadow() {
		return ErrReadOnly
	}
	args := make([]any, len(members))
	for i, m := range members {
		args[i] = m
	}
	return s.client.SRem(ctx, key, args...).Err()
}

// Del executes DEL for the supplied keys. Returns ErrReadOnly when the
// store is a shadow replica. Empty key lists are a no-op. In cluster mode
// every key must hash to the same slot; callers either co-locate via
// `{hashtag}` or call Del once per key.
func (s *Store) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if s.shadow() {
		return ErrReadOnly
	}
	return mapCrossSlotErr(s.client.Del(ctx, keys...).Err())
}

// Scan returns every key matching `match` by iterating SCAN. Used by
// suppressionstore to rebuild the in-memory snapshot; not a hot-path
// operation.
//
// Cluster mode is load-bearing for correctness: go-redis's SCAN on a
// *redis.ClusterClient routes to a single arbitrary master unless the
// pattern carries a `{hashtag}`. The suppression patterns
// (`suppress:{provider_id}:property:*`) have no hashtag, so a naive
// SCAN would silently miss every key on every other master. This
// implementation fans across masters via ForEachMaster and unions.
func (s *Store) Scan(ctx context.Context, match string) ([]string, error) {
	if s.shadow() {
		var out []string
		for _, c := range s.shards {
			keys, err := scanAll(ctx, c, match)
			if err != nil {
				return nil, err
			}
			out = append(out, keys...)
		}
		return out, nil
	}
	if cluster, ok := s.client.(*redis.ClusterClient); ok {
		var (
			mu  sync.Mutex
			out []string
		)
		err := cluster.ForEachMaster(ctx, func(ctx context.Context, c *redis.Client) error {
			keys, err := scanAll(ctx, c, match)
			if err != nil {
				return err
			}
			mu.Lock()
			out = append(out, keys...)
			mu.Unlock()
			return nil
		})
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	return scanAll(ctx, s.client, match)
}

func scanAll(ctx context.Context, c redis.Cmdable, match string) ([]string, error) {
	var (
		out    []string
		cursor uint64
	)
	for {
		keys, next, err := c.Scan(ctx, cursor, match, 256).Result()
		if err != nil {
			return nil, err
		}
		out = append(out, keys...)
		cursor = next
		if cursor == 0 {
			break
		}
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
		return s.client.MSet(ctx, kvs).Err()
	}
	// MSET in Valkey doesn't accept a TTL. With TTL, batch SET-with-expiry per
	// key in a non-atomic pipeline; atomicity is documented as
	// implementation-defined on the targeting.ContextStore interface.
	pipe := s.client.Pipeline()
	for k, v := range kvs {
		pipe.Set(ctx, k, v, ttl)
	}
	_, err := pipe.Exec(ctx)
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
	fieldsAndValues := flattenFields(fields)
	opts := &redis.HSetEXOptions{
		ExpirationType: redis.HSetEXExpirationPXAT,
		ExpirationVal:  expireAt.UnixMilli(),
	}
	return s.client.HSetEXWithArgs(ctx, key, opts, fieldsAndValues...).Err()
}

func (s *Store) SetFieldsBatch(ctx context.Context, batches []fcap.FieldsBatch) error {
	if s.shadow() {
		return ErrReadOnly
	}
	if len(batches) == 0 {
		return nil
	}
	pipe := s.client.Pipeline()
	queued := 0
	for _, b := range batches {
		if len(b.Fields) == 0 {
			continue
		}
		fieldsAndValues := flattenFields(b.Fields)
		opts := &redis.HSetEXOptions{
			ExpirationType: redis.HSetEXExpirationPXAT,
			ExpirationVal:  b.ExpireAt.UnixMilli(),
		}
		pipe.HSetEXWithArgs(ctx, b.Key, opts, fieldsAndValues...)
		queued++
	}
	if queued == 0 {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *Store) FieldExists(ctx context.Context, key, field string) (bool, error) {
	return s.cmdableFor(key).HExists(ctx, key, field).Result()
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
		pipe := s.pipelineFor(group)
		cmds := make([]*redis.BoolCmd, len(indices))
		for j, i := range indices {
			cmds[j] = pipe.HExists(ctx, lookups[i].Key, lookups[i].Field)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		for j, c := range cmds {
			b, err := c.Result()
			if err != nil {
				return fmt.Errorf("redisstore: HEXISTS result %d: %w", indices[j], err)
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

// flattenFields converts a map[string]string to the fieldsAndValues variadic
// pair list that go-redis HSetEX expects.
func flattenFields(fields map[string]string) []string {
	n := len(fields)
	out := make([]string, n+n)
	i := 0
	for f, v := range fields {
		out[i] = f
		out[i+1] = v
		i += 2
	}
	return out
}
