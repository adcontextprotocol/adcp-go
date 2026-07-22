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

// unionFcapStore ORs reads across two backing fcap.Store instances when
// BOTH sides answer. A `true` from either side wins — fcap semantics
// ("capped" is the positive answer) make the union safe: a stale marker
// on the old topology and a fresh marker on the new topology can coexist
// during a Valkey resharding, and the correct enforcement is to consider
// the user capped in either case.
//
// When one side errors, the wrapper propagates the error rather than
// returning the survivor's answer. This preserves the steady-state
// safety bias: at the service layer, an fcap store error runs
// failClosedFcap (every package treated as capped) and stamps
// outcome=error on the stage metric. Masking a single-side error with
// the survivor's answer would let a `false` from the not-holding-the-
// marker side leak through as "not capped" and serve past the cap. Both-
// side outages return the joined error.
//
// Writes go to the primary only; the identity-agent is a reader on the
// request path. The frequency-writer service writes to the primary
// Valkey Cluster directly and is unaffected by this wrapper.
//
// Wired inline in buildBundle (see setup.go) when a fallback config is
// supplied. The full operator runbook — when to turn this on, when to
// remove it — lives at docs/valkey-resharding.md.
type unionFcapStore struct {
	primary  fcap.Store
	fallback fcap.Store
}

func (u *unionFcapStore) FieldExists(ctx context.Context, key, field string) (bool, error) {
	got, err := u.FieldExistsBatch(ctx, []fcap.FieldLookup{{Key: key, Field: field}})
	if err != nil {
		return false, err
	}
	if len(got) == 0 {
		// Defensive: FieldExistsBatch above returned nil+nil for empty
		// input; single-lookup input always has one result. A
		// zero-length result with nil error is a contract violation.
		return false, errors.New("union fcap store: FieldExistsBatch returned empty result for single lookup")
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

	if primErr != nil || fallErr != nil {
		// Fail-closed: any single-side error propagates. runFcapStage
		// runs failClosedFcap on error (every package treated as
		// capped) and stamps outcome=error on the stage metric. Masking
		// the error with the survivor's answer would let a `false` from
		// the side that DOESN'T hold the marker leak through as "not
		// capped" and serve past the cap.
		return nil, errors.Join(primErr, fallErr)
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
// instances when both sides answer. Same union rationale as
// unionFcapStore, and same error-propagation rationale: a single-side
// error propagates so runAudienceStage's fail-closed handling fires
// (every package with a segment rule marked rejected). Masking the error
// would let the survivor's per-audience `false`/`true` answer stand for
// inclusion / exclusion rules ambiguously — audience is not monotone
// (HDelBatch supports removal), so the safe direction differs per rule
// shape (anyOf vs noneOf) and can't be inferred at this layer.
//
// Note that audience membership is not monotone at the value level
// either: a Remove writes a DEL that must propagate to BOTH shadows
// before the OR reads `false`. See docs/valkey-resharding.md for the
// stale-membership window this creates and how to bound it.
type unionAudienceStore struct {
	primary  audience.Store
	fallback audience.Store
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

	if primErr != nil || fallErr != nil {
		// Fail-closed: any single-side error propagates so
		// runAudienceStage runs its rejected-all-with-rules handling.
		// See the type comment above for why single-side masking is
		// unsafe under audience's rule-shape ambiguity.
		return nil, errors.Join(primErr, fallErr)
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
