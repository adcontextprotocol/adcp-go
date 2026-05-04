// Package redisstore provides Valkey-backed implementations of
// targeting.Store and targeting/fcap.Store using github.com/redis/go-redis/v9.
//
// One Store wraps a single go-redis client and satisfies both interfaces, so
// callers share connection state between the targeting engine and the
// frequency-cap service.
package redisstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
)

var (
	_ targeting.Store = (*Store)(nil)
	_ fcap.Store      = (*Store)(nil)
)

// Store wraps a go-redis client and implements targeting.Store and fcap.Store.
type Store struct {
	client redis.UniversalClient
}

// New constructs a Store from a go-redis client. Accepts *redis.Client,
// *redis.ClusterClient, or any redis.UniversalClient.
func New(client redis.UniversalClient) *Store {
	return &Store{client: client}
}

// --- targeting.Store ---

func (s *Store) SetIsMember(ctx context.Context, key, member string) (bool, error) {
	return s.client.SIsMember(ctx, key, member).Result()
}

func (s *Store) SetIntersect(ctx context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	return s.client.SInter(ctx, keys...).Result()
}

func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (s *Store) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) SetMembers(ctx context.Context, key string) ([]string, error) {
	res, err := s.client.SMembers(ctx, key).Result()
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
	results, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	if len(results) != len(keys) {
		return nil, fmt.Errorf("redisstore: MGET returned %d results for %d keys", len(results), len(keys))
	}
	out := make([]string, len(results))
	for i, r := range results {
		if r == nil {
			continue
		}
		if str, ok := r.(string); ok {
			out[i] = str
		}
	}
	return out, nil
}

func (s *Store) MSet(ctx context.Context, kvs map[string]string, ttl time.Duration) error {
	if len(kvs) == 0 {
		return nil
	}
	if ttl <= 0 {
		args := make([]any, 0, 2*len(kvs))
		for k, v := range kvs {
			args = append(args, k, v)
		}
		return s.client.MSet(ctx, args...).Err()
	}
	// MSET in Valkey doesn't accept a TTL. With TTL, batch SET-with-expiry per
	// key in a non-atomic pipeline; atomicity is documented as
	// implementation-defined on the targeting.Store interface.
	pipe := s.client.Pipeline()
	for k, v := range kvs {
		pipe.Set(ctx, k, v, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// --- fcap.Store ---

func (s *Store) SetFields(ctx context.Context, key string, fields map[string]string, expireAt time.Time) error {
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
	return s.client.HExists(ctx, key, field).Result()
}

func (s *Store) FieldExistsBatch(ctx context.Context, lookups []fcap.FieldLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	pipe := s.client.Pipeline()
	cmds := make([]*redis.BoolCmd, len(lookups))
	for i, l := range lookups {
		cmds[i] = pipe.HExists(ctx, l.Key, l.Field)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	out := make([]bool, len(lookups))
	for i, c := range cmds {
		b, err := c.Result()
		if err != nil {
			return nil, fmt.Errorf("redisstore: HEXISTS result %d: %w", i, err)
		}
		out[i] = b
	}
	return out, nil
}

// flattenFields converts a map[string]string to the fieldsAndValues variadic
// pair list that go-redis HSetEX expects.
func flattenFields(fields map[string]string) []string {
	out := make([]string, 0, 2*len(fields))
	for f, v := range fields {
		out = append(out, f, v)
	}
	return out
}
