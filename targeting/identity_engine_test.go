package targeting

import (
	"context"
	"errors"
	"testing"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestEvaluateIdentityResolved_AudienceStoreErrorPropagates locks in the
// contract runAudienceStage relies on for fail-closed behavior. If the
// engine swallowed the audience store's error and defaulted to "member
// of nothing", a NoneOf exclusion rule (consent-withdrawal / brand-
// safety suppression) would find the user "not in any of these
// audiences" → not excluded → served. The engine must surface the
// error so the caller can reject packages carrying segment rules.
func TestEvaluateIdentityResolved_AudienceStoreErrorPropagates(t *testing.T) {
	storeErr := errors.New("audience store unavailable")
	svc := audience.New(&erroringAudienceStore{err: storeErr})
	engine := NewIdentityEngine(IdentityEngineConfig{Audience: svc})

	resolved := &ResolvedPackages{
		IdentityConfigs: map[string]*PackageIdentityConfig{
			"pkg-brand-safety": {
				TargetSegments: &SegmentRule{
					NoneOf: []string{"suppression-list-A"},
				},
			},
		},
	}
	req := &tmproto.IdentityMatchRequest{
		RequestID:  "r1",
		PackageIDs: []string{"pkg-brand-safety"},
		Identities: []tmproto.IdentityToken{{UIDType: "maid", UserToken: "u1"}},
	}

	_, err := engine.EvaluateIdentityResolved(context.Background(), resolved, req)
	if err == nil {
		t.Fatal("expected engine to propagate audience store error; got nil (would fail open on NoneOf rules)")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped store error, got %v", err)
	}
}

// TestEvaluateIdentityResolved_NoAudienceServiceNoError verifies the
// "no audience configured" path stays a nil-error empty read, distinct
// from the "audience configured but broken" path above.
func TestEvaluateIdentityResolved_NoAudienceServiceNoError(t *testing.T) {
	engine := NewIdentityEngine(IdentityEngineConfig{}) // no audience.Service

	resolved := &ResolvedPackages{
		IdentityConfigs: map[string]*PackageIdentityConfig{
			"pkg-plain": {}, // no segment rule
		},
	}
	req := &tmproto.IdentityMatchRequest{
		RequestID:  "r1",
		PackageIDs: []string{"pkg-plain"},
		Identities: []tmproto.IdentityToken{{UIDType: "maid", UserToken: "u1"}},
	}

	result, err := engine.EvaluateIdentityResolved(context.Background(), resolved, req)
	if err != nil {
		t.Fatalf("no audience should not error: %v", err)
	}
	if len(result.Eligibility) != 1 || !result.Eligibility[0].Eligible {
		t.Errorf("expected eligible package, got %+v", result.Eligibility)
	}
}

// erroringAudienceStore is an audience.Store that returns err on every
// call. Local to this test file — the audience package doesn't ship an
// exported mock.
type erroringAudienceStore struct{ err error }

func (e *erroringAudienceStore) HSetBatch(context.Context, []audience.HSetItem) error {
	return e.err
}

func (e *erroringAudienceStore) HExistsBatch(_ context.Context, lookups []audience.HLookup) ([]bool, error) {
	return nil, e.err
}

func (e *erroringAudienceStore) HGetAll(context.Context, string) (map[string]string, error) {
	return nil, e.err
}

func (e *erroringAudienceStore) HGetAllBatch(_ context.Context, keys []string) ([]map[string]string, error) {
	return nil, e.err
}

func (e *erroringAudienceStore) HDelBatch(context.Context, []audience.HDelItem) error {
	return e.err
}

