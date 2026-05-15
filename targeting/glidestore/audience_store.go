package glidestore

import (
	"context"
	"fmt"

	"github.com/valkey-io/valkey-glide/go/v2/pipeline"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
)

var _ audience.Store = (*Store)(nil)

// HSetBatch performs HSET for multiple (key, field, value) triples in
// one pipelined round-trip. Returns ErrReadOnly in shadow-shards mode.
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
	batch := pipeline.NewStandaloneBatch(false)
	for key, fields := range grouped {
		batch.HSet(key, fields)
	}
	_, err := s.client.Exec(ctx, *batch, true)
	return err
}

// HExistsBatch checks one (key, field) pair per lookup, returning
// results in the same order. In shadow mode the per-shard batches run
// in parallel.
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

// HGetAll returns every (field, value) under key. Empty map for missing
// keys.
func (s *Store) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	res, err := s.clientFor(key).HGetAll(ctx, key)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return map[string]string{}, nil
	}
	return res, nil
}

// HGetAllBatch returns HGETALL results for each key in input order.
// Missing keys produce non-nil empty maps at their index.
func (s *Store) HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]map[string]string, len(keys))
	byGroup := s.groupKeys(keys)
	err := s.fanOut(ctx, byGroup, func(ctx context.Context, group int, indices []int) error {
		batch := pipeline.NewStandaloneBatch(false)
		for _, i := range indices {
			batch.HGetAll(keys[i])
		}
		results, err := s.clientForGroup(group).Exec(ctx, *batch, true)
		if err != nil {
			return err
		}
		if len(results) != len(indices) {
			return fmt.Errorf("glidestore: HGETALL batch returned %d results, expected %d", len(results), len(indices))
		}
		for j, r := range results {
			switch v := r.(type) {
			case map[string]string:
				out[indices[j]] = v
			case nil:
				out[indices[j]] = map[string]string{}
			default:
				return fmt.Errorf("glidestore: HGETALL result %d: expected map[string]string, got %T", indices[j], r)
			}
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
	batch := pipeline.NewStandaloneBatch(false)
	queued := 0
	for _, it := range items {
		if len(it.Fields) == 0 {
			continue
		}
		batch.HDel(it.Key, it.Fields)
		queued++
	}
	if queued == 0 {
		return nil
	}
	_, err := s.client.Exec(ctx, *batch, true)
	return err
}
