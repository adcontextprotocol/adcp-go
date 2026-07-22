// The union-store adapters in this file are the reader-side machinery
// that lets identity-agent survive a shard-count change on its Valkey
// backends (fcap or audience) without dropping reads. The full runbook
// — when to enable the fallback, how to reshard, how to remove the
// fallback — lives at docs/valkey-resharding.md at the repo root.
package identityagent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
)

// unionFcapStore ORs reads across two backing fcap.Store instances. A `true`
// from either side wins — fcap semantics ("capped" is the positive answer)
// make the union safe: a stale marker on the old topology and a fresh
// marker on the new topology can coexist during a Valkey resharding, and
// the correct enforcement is to consider the user capped in either case.
//
// Writes go to the primary only; the identity-agent is a reader here, so
// this path is exercised only by tests. The frequency-writer service
// writes to the primary Valkey Cluster directly and is unaffected by this
// wrapper.
//
// Constructed by [buildFcapStore] when a fallback config is supplied.
type unionFcapStore struct {
	primary  fcap.Store
	fallback fcap.Store
	recorder Recorder
}

func (u *unionFcapStore) FieldExists(ctx context.Context, key, field string) (bool, error) {
	got, err := u.FieldExistsBatch(ctx, []fcap.FieldLookup{{Key: key, Field: field}})
	if err != nil {
		return false, err
	}
	return got[0], nil
}

func (u *unionFcapStore) FieldExistsBatch(ctx context.Context, lookups []fcap.FieldLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	var (
		prim, fall       []bool
		primErr, fallErr error
		wg               sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		prim, primErr = u.primary.FieldExistsBatch(ctx, lookups)
	}()
	go func() {
		defer wg.Done()
		fall, fallErr = u.fallback.FieldExistsBatch(ctx, lookups)
	}()
	wg.Wait()

	switch {
	case primErr != nil && fallErr != nil:
		// Both sides failed. Surface the primary error — that's the
		// side we consider authoritative for post-migration reads.
		return nil, errors.Join(primErr, fallErr)
	case primErr != nil:
		// Primary transient-broken; use fallback alone. Record so
		// operators see the imbalance during a fallback-only window.
		if u.recorder != nil {
			u.recorder.StoreError(ctx, StageFCap)
		}
		return fall, nil
	case fallErr != nil:
		// Fallback broken; primary answer is authoritative.
		if u.recorder != nil {
			u.recorder.StoreError(ctx, StageFCap)
		}
		return prim, nil
	}

	if len(prim) != len(lookups) || len(fall) != len(lookups) {
		return nil, errors.New("union fcap store: backing result length mismatch")
	}
	out := make([]bool, len(lookups))
	for i := range out {
		out[i] = prim[i] || fall[i]
	}
	return out, nil
}

// Writes: primary only. Fallback is a read-view of the pre-migration
// topology and never receives new writes.
func (u *unionFcapStore) SetFields(ctx context.Context, key string, fields map[string]string, expireAt time.Time) error {
	return u.primary.SetFields(ctx, key, fields, expireAt)
}

func (u *unionFcapStore) SetFieldsBatch(ctx context.Context, batches []fcap.FieldsBatch) error {
	return u.primary.SetFieldsBatch(ctx, batches)
}

// unionAudienceStore ORs HEXISTS reads across two backing audience.Store
// instances. Same rationale as unionFcapStore: audience membership is a
// positive-truth predicate, so a hit on either side is a real hit; a miss
// on both is a real miss. Reads that touch data that has just migrated
// (present on one side and pending on the other) still surface the
// correct answer through the union window.
type unionAudienceStore struct {
	primary  audience.Store
	fallback audience.Store
	recorder Recorder
}

func (u *unionAudienceStore) HExistsBatch(ctx context.Context, lookups []audience.HLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	var (
		prim, fall       []bool
		primErr, fallErr error
		wg               sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		prim, primErr = u.primary.HExistsBatch(ctx, lookups)
	}()
	go func() {
		defer wg.Done()
		fall, fallErr = u.fallback.HExistsBatch(ctx, lookups)
	}()
	wg.Wait()

	switch {
	case primErr != nil && fallErr != nil:
		return nil, errors.Join(primErr, fallErr)
	case primErr != nil:
		if u.recorder != nil {
			u.recorder.StoreError(ctx, StageAudience)
		}
		return fall, nil
	case fallErr != nil:
		if u.recorder != nil {
			u.recorder.StoreError(ctx, StageAudience)
		}
		return prim, nil
	}

	if len(prim) != len(lookups) || len(fall) != len(lookups) {
		return nil, errors.New("union audience store: backing result length mismatch")
	}
	out := make([]bool, len(lookups))
	for i := range out {
		out[i] = prim[i] || fall[i]
	}
	return out, nil
}

// HGetAll / HGetAllBatch: read from primary only. Union semantics for the
// full-hash case is ambiguous (which side's score wins when both hold a
// field?), and the identity-match read path uses HExistsBatch — not
// HGetAll — so the extra work isn't worth the ambiguity. Callers that
// need mid-migration HGetAll consistency should query the primary Valkey
// Cluster directly.
func (u *unionAudienceStore) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return u.primary.HGetAll(ctx, key)
}

func (u *unionAudienceStore) HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error) {
	return u.primary.HGetAllBatch(ctx, keys)
}

// Writes: primary only. See unionFcapStore for rationale.
func (u *unionAudienceStore) HSetBatch(ctx context.Context, items []audience.HSetItem) error {
	return u.primary.HSetBatch(ctx, items)
}

func (u *unionAudienceStore) HDelBatch(ctx context.Context, items []audience.HDelItem) error {
	return u.primary.HDelBatch(ctx, items)
}
