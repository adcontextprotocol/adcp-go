package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store abstracts the sorted-set and set operations needed by the identity agent.
// RedisStore wraps go-redis; InMemoryStore provides a fallback when Valkey is down.
type Store interface {
	ZAdd(ctx context.Context, key string, score float64, member string) error
	ZCount(ctx context.Context, key string, min, max float64) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
	SIsMember(ctx context.Context, key, member string) (bool, error)
	SAdd(ctx context.Context, key string, members ...interface{}) error
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Pipeline(ctx context.Context) StorePipeline
	Ping(ctx context.Context) error
}

// StorePipeline batches commands for execution.
type StorePipeline interface {
	ZAdd(ctx context.Context, key string, score float64, member string)
	Expire(ctx context.Context, key string, ttl time.Duration)
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration)
	Exec(ctx context.Context) error
}

// --- Redis Implementation ---

// RedisStore wraps go-redis.
type RedisStore struct {
	rdb *redis.Client
}

func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb}
}

func (s *RedisStore) ZAdd(ctx context.Context, key string, score float64, member string) error {
	return s.rdb.ZAdd(ctx, key, redis.Z{Score: score, Member: member}).Err()
}

func (s *RedisStore) ZCount(ctx context.Context, key string, min, max float64) (int64, error) {
	minStr := fmt.Sprintf("%f", min)
	maxStr := "+inf"
	if max < 1e18 {
		maxStr = fmt.Sprintf("%f", max)
	}
	return s.rdb.ZCount(ctx, key, minStr, maxStr).Result()
}

func (s *RedisStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.rdb.Expire(ctx, key, ttl).Err()
}

func (s *RedisStore) SIsMember(ctx context.Context, key, member string) (bool, error) {
	return s.rdb.SIsMember(ctx, key, member).Result()
}

func (s *RedisStore) SAdd(ctx context.Context, key string, members ...interface{}) error {
	return s.rdb.SAdd(ctx, key, members...).Err()
}

func (s *RedisStore) Get(ctx context.Context, key string) (string, error) {
	val, err := s.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

func (s *RedisStore) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, value, ttl).Err()
}

func (s *RedisStore) Pipeline(_ context.Context) StorePipeline {
	return &RedisPipeline{pipe: s.rdb.Pipeline()}
}

func (s *RedisStore) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}

// RedisPipeline wraps go-redis pipeline.
type RedisPipeline struct {
	pipe redis.Pipeliner
}

func (p *RedisPipeline) ZAdd(ctx context.Context, key string, score float64, member string) {
	p.pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: member})
}

func (p *RedisPipeline) Expire(ctx context.Context, key string, ttl time.Duration) {
	p.pipe.Expire(ctx, key, ttl)
}

func (p *RedisPipeline) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) {
	p.pipe.Set(ctx, key, value, ttl)
}

func (p *RedisPipeline) Exec(ctx context.Context) error {
	_, err := p.pipe.Exec(ctx)
	return err
}

// --- In-Memory Implementation ---

type zsetEntry struct {
	score  float64
	member string
}

type memKey struct {
	value   interface{}
	expires time.Time
}

// InMemoryStore provides a fallback when Valkey is unavailable.
type InMemoryStore struct {
	mu    sync.RWMutex
	zsets map[string][]zsetEntry
	sets  map[string]map[string]struct{}
	kvs   map[string]memKey
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		zsets: make(map[string][]zsetEntry),
		sets:  make(map[string]map[string]struct{}),
		kvs:   make(map[string]memKey),
	}
}

func (s *InMemoryStore) ZAdd(_ context.Context, key string, score float64, member string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zsets[key] = append(s.zsets[key], zsetEntry{score: score, member: member})
	return nil
}

func (s *InMemoryStore) ZCount(_ context.Context, key string, min, max float64) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int64
	for _, e := range s.zsets[key] {
		if e.score >= min && (max >= 1e18 || e.score <= max) {
			count++
		}
	}
	return count, nil
}

func (s *InMemoryStore) Expire(_ context.Context, _ string, _ time.Duration) error {
	return nil // TTL not implemented for in-memory (acceptable for fallback)
}

func (s *InMemoryStore) SIsMember(_ context.Context, key, member string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.sets[key]
	if !ok {
		return false, nil
	}
	_, found := set[member]
	return found, nil
}

func (s *InMemoryStore) SAdd(_ context.Context, key string, members ...interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.sets[key]
	if !ok {
		set = make(map[string]struct{})
		s.sets[key] = set
	}
	for _, m := range members {
		set[fmt.Sprintf("%v", m)] = struct{}{}
	}
	return nil
}

func (s *InMemoryStore) Get(_ context.Context, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	kv, ok := s.kvs[key]
	if !ok {
		return "", nil
	}
	if !kv.expires.IsZero() && time.Now().After(kv.expires) {
		return "", nil
	}
	return fmt.Sprintf("%v", kv.value), nil
}

func (s *InMemoryStore) Set(_ context.Context, key string, value interface{}, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	s.kvs[key] = memKey{value: value, expires: expires}
	return nil
}

func (s *InMemoryStore) Pipeline(_ context.Context) StorePipeline {
	return &InMemoryPipeline{store: s}
}

func (s *InMemoryStore) Ping(_ context.Context) error {
	return nil
}

// ZEntries returns sorted entries for a key (for testing).
func (s *InMemoryStore) ZEntries(key string) []zsetEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := make([]zsetEntry, len(s.zsets[key]))
	copy(entries, s.zsets[key])
	sort.Slice(entries, func(i, j int) bool { return entries[i].score < entries[j].score })
	return entries
}

// InMemoryPipeline collects operations and executes them sequentially.
type InMemoryPipeline struct {
	store *InMemoryStore
	ops   []func(context.Context) error
}

func (p *InMemoryPipeline) ZAdd(_ context.Context, key string, score float64, member string) {
	p.ops = append(p.ops, func(ctx context.Context) error {
		return p.store.ZAdd(ctx, key, score, member)
	})
}

func (p *InMemoryPipeline) Expire(_ context.Context, key string, ttl time.Duration) {
	p.ops = append(p.ops, func(ctx context.Context) error {
		return p.store.Expire(ctx, key, ttl)
	})
}

func (p *InMemoryPipeline) Set(_ context.Context, key string, value interface{}, ttl time.Duration) {
	p.ops = append(p.ops, func(ctx context.Context) error {
		return p.store.Set(ctx, key, value, ttl)
	})
}

func (p *InMemoryPipeline) Exec(ctx context.Context) error {
	for _, op := range p.ops {
		if err := op(ctx); err != nil {
			return err
		}
	}
	return nil
}
