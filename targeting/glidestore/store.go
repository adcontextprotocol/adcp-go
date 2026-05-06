// Package glidestore provides Valkey-backed implementations of
// targeting.Store and targeting/fcap.Store using valkey-glide/go/v2.
//
// One Store wraps a single glide client and satisfies both interfaces, so
// callers share connection state between the targeting engine and the
// frequency-cap service.
package glidestore

import (
	"context"
	"fmt"
	"time"

	glide "github.com/valkey-io/valkey-glide/go/v2"
	"github.com/valkey-io/valkey-glide/go/v2/options"
	"github.com/valkey-io/valkey-glide/go/v2/pipeline"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
)

var (
	_ targeting.Store = (*Store)(nil)
	_ fcap.Store      = (*Store)(nil)
)

// Store wraps a glide client and implements targeting.Store and fcap.Store.
type Store struct {
	client *glide.Client
}

// New constructs a Store from a glide client.
func New(client *glide.Client) *Store {
	return &Store{client: client}
}

// --- targeting.Store ---

func (s *Store) SetIsMember(ctx context.Context, key, member string) (bool, error) {
	return s.client.SIsMember(ctx, key, member)
}

func (s *Store) SetIntersect(ctx context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	set, err := s.client.SInter(ctx, keys)
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
	res, err := s.client.Get(ctx, key)
	if err != nil {
		return "", false, err
	}
	if res.IsNil() {
		return "", false, nil
	}
	return res.Value(), true, nil
}

func (s *Store) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl <= 0 {
		_, err := s.client.Set(ctx, key, value)
		return err
	}
	opts := *options.NewSetOptions().SetExpiry(options.NewExpiryIn(ttl))
	_, err := s.client.SetWithOptions(ctx, key, value, opts)
	return err
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.client.Exists(ctx, []string{key})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) SetMembers(ctx context.Context, key string) ([]string, error) {
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

func (s *Store) MGet(ctx context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	results, err := s.client.MGet(ctx, keys)
	if err != nil {
		return nil, err
	}
	if len(results) != len(keys) {
		return nil, fmt.Errorf("glidestore: MGET returned %d results for %d keys", len(results), len(keys))
	}
	out := make([]string, len(results))
	for i, r := range results {
		if r.IsNil() {
			continue
		}
		out[i] = r.Value()
	}
	return out, nil
}

func (s *Store) MSet(ctx context.Context, kvs map[string]string, ttl time.Duration) error {
	if len(kvs) == 0 {
		return nil
	}
	if ttl <= 0 {
		_, err := s.client.MSet(ctx, kvs)
		return err
	}
	// MSET in Valkey doesn't accept a TTL. With TTL, we batch SET+EXPIRE per
	// key in a single non-atomic batch.
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
	if len(fields) == 0 {
		return nil
	}
	opts := options.NewHSetExOptions().SetExpiry(options.NewExpiryAt(expireAt))
	_, err := s.client.HSetEx(ctx, key, fields, opts)
	return err
}

func (s *Store) SetFieldsBatch(ctx context.Context, batches []fcap.FieldsBatch) error {
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
	return s.client.HExists(ctx, key, field)
}

func (s *Store) FieldExistsBatch(ctx context.Context, lookups []fcap.FieldLookup) ([]bool, error) {
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
