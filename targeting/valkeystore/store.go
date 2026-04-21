// Package valkeystore provides a targeting.Store backed by Valkey or Redis.
package valkeystore

import (
	"context"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/redis/go-redis/v9"
)

// Ensure Store satisfies the interface at compile time.
var _ targeting.Store = (*Store)(nil)

// Store wraps a go-redis client to satisfy targeting.Store.
type Store struct {
	rdb redis.Cmdable
}

// New creates a Store from a go-redis client.
// Accepts *redis.Client, *redis.ClusterClient, or any redis.Cmdable.
func New(rdb redis.Cmdable) *Store {
	return &Store{rdb: rdb}
}

func (s *Store) SetIsMember(ctx context.Context, key, member string) (bool, error) {
	return s.rdb.SIsMember(ctx, key, member).Result()
}

func (s *Store) SetIntersect(ctx context.Context, keys ...string) ([]string, error) {
	return s.rdb.SInter(ctx, keys...).Result()
}

func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := s.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (s *Store) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, value, ttl).Err()
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) SetMembers(ctx context.Context, key string) ([]string, error) {
	result, err := s.rdb.SMembers(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return result, err
}

func (s *Store) MGet(ctx context.Context, keys ...string) ([]string, error) {
	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	results := make([]string, len(vals))
	for i, v := range vals {
		if s, ok := v.(string); ok {
			results[i] = s
		}
	}
	return results, nil
}
