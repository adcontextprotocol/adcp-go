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
// single HSET command. Returns ErrReadOnly in shadow-shards mode.
func (s *Store) HSetBatch(ctx context.Context, items []audience.HSetItem) error {
	if s.shadow() {
		return ErrReadOnly
	}
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

// HExistsBatch checks one (key, field) pair per lookup, returning results
// in the same order. Pipelined per shard; in shadow mode the per-shard
// pipelines run in parallel.
func (s *Store) HExistsBatch(ctx context.Context, lookups []audience.HLookup) ([]bool, error) {
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

// HGetAll returns every (field, value) under key. Empty map for missing
// keys — go-redis returns an empty map rather than nil.
func (s *Store) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return s.cmdableFor(key).HGetAll(ctx, key).Result()
}

// HGetAllBatch returns HGETALL results for each key in input order.
// Missing keys produce empty maps at their index.
func (s *Store) HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]map[string]string, len(keys))
	byGroup := s.groupKeys(keys)
	err := s.fanOut(ctx, byGroup, func(ctx context.Context, group int, indices []int) error {
		pipe := s.pipelineFor(group)
		cmds := make([]*redis.MapStringStringCmd, len(indices))
		for j, i := range indices {
			cmds[j] = pipe.HGetAll(ctx, keys[i])
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		for j, c := range cmds {
			m, err := c.Result()
			if err != nil {
				return fmt.Errorf("redisstore: HGETALL result %d: %w", indices[j], err)
			}
			out[indices[j]] = m
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// HDelBatch performs HDEL for multiple (key, fields) pairs in one
// pipelined round-trip. Returns ErrReadOnly in shadow-shards mode.
func (s *Store) HDelBatch(ctx context.Context, items []audience.HDelItem) error {
	if s.shadow() {
		return ErrReadOnly
	}
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
