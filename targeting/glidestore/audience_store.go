package glidestore

import (
	"context"
	"fmt"

	"github.com/valkey-io/valkey-glide/go/v2/pipeline"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
)

var _ audience.Store = (*Store)(nil)

// HSet sets a single hash field.
func (s *Store) HSet(ctx context.Context, key, field, value string) error {
	_, err := s.client.HSet(ctx, key, map[string]string{field: value})
	return err
}

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
	batch := pipeline.NewStandaloneBatch(false)
	for key, fields := range grouped {
		batch.HSet(key, fields)
	}
	_, err := s.client.Exec(ctx, *batch, true)
	return err
}

// HExists reports whether field exists under key.
func (s *Store) HExists(ctx context.Context, key, field string) (bool, error) {
	return s.client.HExists(ctx, key, field)
}

// HExistsBatch checks one (key, field) pair per lookup, returning results in
// the same order. Pipelined.
func (s *Store) HExistsBatch(ctx context.Context, lookups []audience.HLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	batch := pipeline.NewStandaloneBatch(false)
	for _, l := range lookups {
		batch.HExists(l.Key, l.Field)
	}
	results, err := s.client.Exec(ctx, *batch, true)
	if err != nil {
		return nil, err
	}
	if len(results) != len(lookups) {
		return nil, fmt.Errorf("glidestore: HEXISTS batch returned %d results, expected %d", len(results), len(lookups))
	}
	out := make([]bool, len(results))
	for i, r := range results {
		// Validated against valkey-glide/go/v2 v2.3.1: HEXISTS in a batch
		// arrives as a plain bool. Surface the actual type if that ever
		// changes so the failure mode is loud rather than silently false.
		b, ok := r.(bool)
		if !ok {
			return nil, fmt.Errorf("glidestore: HEXISTS result %d: expected bool, got %T", i, r)
		}
		out[i] = b
	}
	return out, nil
}

// HGetAll returns every (field, value) under key. Empty map for missing keys.
func (s *Store) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	res, err := s.client.HGetAll(ctx, key)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return map[string]string{}, nil
	}
	return res, nil
}

// HGetAllBatch returns HGETALL results for each key in input order. Missing
// keys produce empty maps at their index.
func (s *Store) HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	batch := pipeline.NewStandaloneBatch(false)
	for _, k := range keys {
		batch.HGetAll(k)
	}
	results, err := s.client.Exec(ctx, *batch, true)
	if err != nil {
		return nil, err
	}
	if len(results) != len(keys) {
		return nil, fmt.Errorf("glidestore: HGETALL batch returned %d results, expected %d", len(results), len(keys))
	}
	out := make([]map[string]string, len(results))
	for i, r := range results {
		switch v := r.(type) {
		case map[string]string:
			out[i] = v
		case nil:
			out[i] = map[string]string{}
		default:
			return nil, fmt.Errorf("glidestore: HGETALL result %d: expected map[string]string, got %T", i, r)
		}
	}
	return out, nil
}

// HDel removes the named fields from key.
func (s *Store) HDel(ctx context.Context, key string, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	_, err := s.client.HDel(ctx, key, fields)
	return err
}

// SAdd adds members to the set at key.
func (s *Store) SAdd(ctx context.Context, key string, members []string) error {
	if len(members) == 0 {
		return nil
	}
	_, err := s.client.SAdd(ctx, key, members)
	return err
}

// SRem removes members from the set at key.
func (s *Store) SRem(ctx context.Context, key string, members []string) error {
	if len(members) == 0 {
		return nil
	}
	_, err := s.client.SRem(ctx, key, members)
	return err
}

// SMembers returns every member of the set at key. Returns nil for missing keys.
func (s *Store) SMembers(ctx context.Context, key string) ([]string, error) {
	set, err := s.client.SMembers(ctx, key)
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

// Del removes the key entirely.
func (s *Store) Del(ctx context.Context, key string) error {
	_, err := s.client.Del(ctx, []string{key})
	return err
}
