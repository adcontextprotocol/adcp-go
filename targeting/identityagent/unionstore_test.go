package identityagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
)

// ----- fcap union -----

func TestUnionFcapStore_BatchORsReads(t *testing.T) {
	primary := fcap.NewMockStore()
	fallback := fcap.NewMockStore()
	ctx := context.Background()
	// Primary has {A capped, B not capped}. Fallback has {A not capped, B capped}.
	if err := primary.SetFields(ctx, "user-1", map[string]string{"A": "1"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := fallback.SetFields(ctx, "user-1", map[string]string{"B": "1"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	u := &unionFcapStore{primary: primary, fallback: fallback}
	got, err := u.FieldExistsBatch(ctx, []fcap.FieldLookup{
		{Key: "user-1", Field: "A"},
		{Key: "user-1", Field: "B"},
		{Key: "user-1", Field: "C"},
	})
	if err != nil {
		t.Fatalf("union batch: %v", err)
	}
	want := []bool{true, true, false}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("lookup %d: got %v want %v", i, got[i], w)
		}
	}
}

// TestUnionFcapStore_SingleSideErrorPropagates is the behavior that
// keeps the steady-state fail-closed invariant during a fallback window.
// See unionstore.go doc comment: a masked single-side error would let a
// `false` from the side NOT holding the marker serve past the cap.
func TestUnionFcapStore_SingleSideErrorPropagates(t *testing.T) {
	fallback := fcap.NewMockStore()
	ctx := context.Background()
	if err := fallback.SetFields(ctx, "u", map[string]string{"X": "1"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	primaryDown := errors.New("primary down")

	t.Run("primary errors", func(t *testing.T) {
		u := &unionFcapStore{primary: &errFcapStore{err: primaryDown}, fallback: fallback}
		got, err := u.FieldExistsBatch(ctx, []fcap.FieldLookup{{Key: "u", Field: "X"}})
		if err == nil {
			t.Fatal("expected error to propagate on single-side failure")
		}
		if !errors.Is(err, primaryDown) {
			t.Errorf("expected joined error to contain primary err, got %v", err)
		}
		if got != nil {
			t.Errorf("expected nil result on error, got %v", got)
		}
	})

	t.Run("fallback errors", func(t *testing.T) {
		fallbackDown := errors.New("fallback down")
		u := &unionFcapStore{primary: fallback, fallback: &errFcapStore{err: fallbackDown}}
		_, err := u.FieldExistsBatch(ctx, []fcap.FieldLookup{{Key: "u", Field: "X"}})
		if err == nil {
			t.Fatal("expected error to propagate on single-side failure")
		}
		if !errors.Is(err, fallbackDown) {
			t.Errorf("expected joined error to contain fallback err, got %v", err)
		}
	})
}

func TestUnionFcapStore_BothErrorReturnsJoinedError(t *testing.T) {
	primary := &errFcapStore{err: errors.New("primary down")}
	fallback := &errFcapStore{err: errors.New("fallback down")}
	u := &unionFcapStore{primary: primary, fallback: fallback}
	_, err := u.FieldExistsBatch(context.Background(), []fcap.FieldLookup{{Key: "u", Field: "X"}})
	if err == nil {
		t.Fatal("expected error when both sides fail")
	}
	if !errors.Is(err, primary.err) || !errors.Is(err, fallback.err) {
		t.Errorf("expected joined error containing both, got %v", err)
	}
}

// TestUnionFcapStore_FieldExistsSingle exercises the single-key path
// (previously untested) and confirms it defers to FieldExistsBatch.
func TestUnionFcapStore_FieldExistsSingle(t *testing.T) {
	primary := fcap.NewMockStore()
	fallback := fcap.NewMockStore()
	ctx := context.Background()
	_ = fallback.SetFields(ctx, "u", map[string]string{"F": "1"}, time.Now().Add(time.Hour))
	u := &unionFcapStore{primary: primary, fallback: fallback}

	got, err := u.FieldExists(ctx, "u", "F")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected true from union on fallback-only hit")
	}

	got, err = u.FieldExists(ctx, "u", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected false when neither side holds the field")
	}
}

func TestUnionFcapStore_WritesGoToPrimaryOnly(t *testing.T) {
	primary := fcap.NewMockStore()
	fallback := fcap.NewMockStore()
	u := &unionFcapStore{primary: primary, fallback: fallback}
	ctx := context.Background()
	if err := u.SetFields(ctx, "u", map[string]string{"F": "1"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	primHit, _ := primary.FieldExists(ctx, "u", "F")
	fallHit, _ := fallback.FieldExists(ctx, "u", "F")
	if !primHit {
		t.Error("primary should have received the write")
	}
	if fallHit {
		t.Error("fallback should NOT have received the write")
	}
}

// ----- audience union -----

func TestUnionAudienceStore_BatchORsReads(t *testing.T) {
	primary := newMockAudienceStore()
	fallback := newMockAudienceStore()
	ctx := context.Background()
	_ = primary.HSetBatch(ctx, []audience.HSetItem{{Key: "u-1", Field: "aud-A", Value: "1"}})
	_ = fallback.HSetBatch(ctx, []audience.HSetItem{{Key: "u-1", Field: "aud-B", Value: "1"}})

	u := &unionAudienceStore{primary: primary, fallback: fallback}
	got, err := u.HExistsBatch(ctx, []audience.HLookup{
		{Key: "u-1", Field: "aud-A"},
		{Key: "u-1", Field: "aud-B"},
		{Key: "u-1", Field: "aud-C"},
	})
	if err != nil {
		t.Fatalf("union batch: %v", err)
	}
	want := []bool{true, true, false}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("lookup %d: got %v want %v", i, got[i], w)
		}
	}
}

// TestUnionAudienceStore_SingleSideErrorPropagates locks in the
// symmetric fcap contract for audience: a single-side error must NOT
// be masked by returning the survivor's answer. See unionstore.go for
// the fail-closed rationale (NoneOf exclusion rules would fail open
// under an "empty membership" default).
func TestUnionAudienceStore_SingleSideErrorPropagates(t *testing.T) {
	fallback := newMockAudienceStore()
	ctx := context.Background()
	_ = fallback.HSetBatch(ctx, []audience.HSetItem{{Key: "u", Field: "aud", Value: "1"}})
	primaryDown := errors.New("primary audience down")

	t.Run("primary errors", func(t *testing.T) {
		u := &unionAudienceStore{primary: &errAudienceStore{err: primaryDown}, fallback: fallback}
		_, err := u.HExistsBatch(ctx, []audience.HLookup{{Key: "u", Field: "aud"}})
		if err == nil {
			t.Fatal("expected error to propagate; masking would fail-open NoneOf rules")
		}
		if !errors.Is(err, primaryDown) {
			t.Errorf("expected joined error to contain primary err, got %v", err)
		}
	})

	t.Run("fallback errors", func(t *testing.T) {
		fallbackDown := errors.New("fallback audience down")
		u := &unionAudienceStore{primary: fallback, fallback: &errAudienceStore{err: fallbackDown}}
		_, err := u.HExistsBatch(ctx, []audience.HLookup{{Key: "u", Field: "aud"}})
		if err == nil {
			t.Fatal("expected error to propagate on single-side failure")
		}
		if !errors.Is(err, fallbackDown) {
			t.Errorf("expected joined error to contain fallback err, got %v", err)
		}
	})
}

func TestUnionAudienceStore_BothErrorReturnsJoinedError(t *testing.T) {
	primaryDown := errors.New("primary down")
	fallbackDown := errors.New("fallback down")
	u := &unionAudienceStore{
		primary:  &errAudienceStore{err: primaryDown},
		fallback: &errAudienceStore{err: fallbackDown},
	}
	_, err := u.HExistsBatch(context.Background(), []audience.HLookup{{Key: "u", Field: "aud"}})
	if err == nil {
		t.Fatal("expected error when both sides fail")
	}
	if !errors.Is(err, primaryDown) || !errors.Is(err, fallbackDown) {
		t.Errorf("expected joined error containing both, got %v", err)
	}
}

// errAudienceStore returns err from every method — exercises error paths
// that the mock in-memory store can't reach.
type errAudienceStore struct{ err error }

func (e *errAudienceStore) HSetBatch(context.Context, []audience.HSetItem) error { return e.err }
func (e *errAudienceStore) HExistsBatch(context.Context, []audience.HLookup) ([]bool, error) {
	return nil, e.err
}
func (e *errAudienceStore) HGetAll(context.Context, string) (map[string]string, error) {
	return nil, e.err
}
func (e *errAudienceStore) HGetAllBatch(context.Context, []string) ([]map[string]string, error) {
	return nil, e.err
}
func (e *errAudienceStore) HDelBatch(context.Context, []audience.HDelItem) error { return e.err }

func TestUnionAudienceStore_WritesGoToPrimaryOnly(t *testing.T) {
	primary := newMockAudienceStore()
	fallback := newMockAudienceStore()
	u := &unionAudienceStore{primary: primary, fallback: fallback}
	ctx := context.Background()
	if err := u.HSetBatch(ctx, []audience.HSetItem{{Key: "u", Field: "aud", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	prim, _ := primary.HExistsBatch(ctx, []audience.HLookup{{Key: "u", Field: "aud"}})
	fall, _ := fallback.HExistsBatch(ctx, []audience.HLookup{{Key: "u", Field: "aud"}})
	if !prim[0] {
		t.Error("primary should have received the write")
	}
	if fall[0] {
		t.Error("fallback should NOT have received the write")
	}
}

// ----- test doubles -----

// errFcapStore returns err from every method — used to exercise error paths.
type errFcapStore struct{ err error }

func (e *errFcapStore) SetFields(context.Context, string, map[string]string, time.Time) error {
	return e.err
}
func (e *errFcapStore) SetFieldsBatch(context.Context, []fcap.FieldsBatch) error { return e.err }
func (e *errFcapStore) FieldExists(context.Context, string, string) (bool, error) {
	return false, e.err
}
func (e *errFcapStore) FieldExistsBatch(context.Context, []fcap.FieldLookup) ([]bool, error) {
	return nil, e.err
}

// mockAudienceStore is a minimal in-memory audience.Store for tests. Kept
// local because the audience package doesn't ship an exported mock.
type mockAudienceStore struct {
	data map[string]map[string]string
}

func newMockAudienceStore() *mockAudienceStore {
	return &mockAudienceStore{data: make(map[string]map[string]string)}
}

func (m *mockAudienceStore) HSetBatch(_ context.Context, items []audience.HSetItem) error {
	for _, it := range items {
		if _, ok := m.data[it.Key]; !ok {
			m.data[it.Key] = make(map[string]string)
		}
		m.data[it.Key][it.Field] = it.Value
	}
	return nil
}

func (m *mockAudienceStore) HExistsBatch(_ context.Context, lookups []audience.HLookup) ([]bool, error) {
	out := make([]bool, len(lookups))
	for i, l := range lookups {
		if fields, ok := m.data[l.Key]; ok {
			_, ok := fields[l.Field]
			out[i] = ok
		}
	}
	return out, nil
}

func (m *mockAudienceStore) HGetAll(_ context.Context, key string) (map[string]string, error) {
	src := m.data[key]
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out, nil
}

func (m *mockAudienceStore) HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error) {
	out := make([]map[string]string, len(keys))
	for i, k := range keys {
		v, _ := m.HGetAll(ctx, k)
		out[i] = v
	}
	return out, nil
}

func (m *mockAudienceStore) HDelBatch(_ context.Context, items []audience.HDelItem) error {
	for _, it := range items {
		if fields, ok := m.data[it.Key]; ok {
			for _, f := range it.Fields {
				delete(fields, f)
			}
		}
	}
	return nil
}
