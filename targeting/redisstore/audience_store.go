package redisstore

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
)

var _ audience.Store = (*Store)(nil)

// HSetBatch performs HSET for multiple (key, field, value) triples in one
// pipelined round-trip. Items targeting the same key are grouped into a
// single HSET command.
func (s *Store) HSetBatch(ctx context.Context, items []audience.HSetItem) error {
	if len(items) == 0 {
		return nil
	}
	grouped := make(map[string]map[string]string)
	for _, it := range items {
		fields, ok := grouped[it.Key]
		if !ok {
			fields = make(map[string]string)
			grouped[it.Key] = fields
		}
		fields[it.Field] = it.Value
	}
	pipe := s.client.Pipeline()
	for key, fields := range grouped {
		pipe.HSet(ctx, key, fields)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// HExists reports whether field exists under key.
func (s *Store) HExists(ctx context.Context, key, field string) (bool, error) {
	return s.client.HExists(ctx, key, field).Result()
}

// HExistsBatch checks one (key, field) pair per lookup, returning results in
// the same order. Pipelined.
func (s *Store) HExistsBatch(ctx context.Context, lookups []audience.HLookup) ([]bool, error) {
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

// HGetAll returns every (field, value) under key. Empty map for missing keys —
// go-redis returns an empty map rather than nil for unknown keys.
func (s *Store) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return s.client.HGetAll(ctx, key).Result()
}

// HGetAllBatch returns HGETALL results for each key in input order. Missing
// keys produce empty maps at their index.
func (s *Store) HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	pipe := s.client.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(keys))
	for i, k := range keys {
		cmds[i] = pipe.HGetAll(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(keys))
	for i, c := range cmds {
		m, err := c.Result()
		if err != nil {
			return nil, fmt.Errorf("redisstore: HGETALL result %d: %w", i, err)
		}
		out[i] = m
	}
	return out, nil
}

// HDel removes the named fields from key.
func (s *Store) HDel(ctx context.Context, key string, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	return s.client.HDel(ctx, key, fields...).Err()
}

// HDelBatch performs HDEL for multiple (key, fields) pairs in one pipelined
// round-trip.
func (s *Store) HDelBatch(ctx context.Context, items []audience.HDelItem) error {
	if len(items) == 0 {
		return nil
	}
	pipe := s.client.Pipeline()
	queued := 0
	for _, it := range items {
		if len(it.Fields) == 0 {
			continue
		}
		pipe.HDel(ctx, it.Key, it.Fields...)
		queued++
	}
	if queued == 0 {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

// SAdd adds members to the set at key.
func (s *Store) SAdd(ctx context.Context, key string, members []string) error {
	if len(members) == 0 {
		return nil
	}
	args := make([]any, len(members))
	for i, m := range members {
		args[i] = m
	}
	return s.client.SAdd(ctx, key, args...).Err()
}

// SRem removes members from the set at key.
func (s *Store) SRem(ctx context.Context, key string, members []string) error {
	if len(members) == 0 {
		return nil
	}
	args := make([]any, len(members))
	for i, m := range members {
		args[i] = m
	}
	return s.client.SRem(ctx, key, args...).Err()
}

// SMembers returns every member of the set at key. Returns nil for missing keys.
func (s *Store) SMembers(ctx context.Context, key string) ([]string, error) {
	res, err := s.client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, nil
	}
	return res, nil
}

// Del removes the key entirely.
func (s *Store) Del(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}
