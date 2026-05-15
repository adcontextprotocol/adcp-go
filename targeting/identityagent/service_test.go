package identityagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// memSource is an identityconfig.Source backed by a fixed in-memory map.
// Sufficient for driving NewService's ConfigService dependency in unit tests.
type memSource struct {
	entries []identityconfig.Entry
}

func (s *memSource) LoadAll(_ context.Context) (identityconfig.Snapshot, error) {
	return identityconfig.Snapshot{
		Configs:       s.entries,
		LastUpdatedAt: time.Unix(0, 0),
	}, nil
}

func (s *memSource) LoadUpdatedAfter(_ context.Context, _ time.Time) (identityconfig.Delta, error) {
	return identityconfig.Delta{LastUpdatedAt: time.Unix(0, 0)}, nil
}

// newTestConfigService builds and starts an identityconfig.Service seeded
// with the supplied entries.
func newTestConfigService(t *testing.T, entries []identityconfig.Entry) *identityconfig.Service {
	t.Helper()
	src := &memSource{entries: entries}
	svc, err := identityconfig.New(src, time.Minute)
	if err != nil {
		t.Fatalf("identityconfig.New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

// newTestService builds a Service over in-memory fcap and audience stores,
// seeding fcap with the supplied (identity, seller, pkg) tuples as capped
// (TTL 1h) and audience with the supplied (identity, audience) memberships.
func newTestService(t *testing.T, opts testServiceOptions) *Service {
	t.Helper()
	fcapStore := fcap.NewMockStore()
	fcapSvc := fcap.New(fcapStore)
	if len(opts.cappedTuples) > 0 {
		expireAt := time.Now().Add(time.Hour)
		for _, tup := range opts.cappedTuples {
			err := fcapSvc.RecordCap(context.Background(), tup.identity,
				[]fcap.Field{{SellerAgentURL: tup.seller, PackageID: tup.pkg}},
				expireAt)
			if err != nil {
				t.Fatalf("RecordCap: %v", err)
			}
		}
	}

	audStore := audience.NewMockStore()
	audSvc := audience.New(audStore)
	if len(opts.memberships) > 0 {
		upserts := make([]audience.AudienceUpsert, 0, len(opts.memberships))
		byAudience := make(map[string][]audience.Member)
		for _, m := range opts.memberships {
			byAudience[m.audienceID] = append(byAudience[m.audienceID], audience.Member{UserToken: m.userToken})
		}
		for audID, members := range byAudience {
			upserts = append(upserts, audience.AudienceUpsert{AudienceID: audID, Add: members})
		}
		if err := audSvc.UpsertBatch(context.Background(), upserts); err != nil {
			t.Fatalf("UpsertBatch: %v", err)
		}
	}

	engine := targeting.NewIdentityEngine(targeting.IdentityEngineConfig{
		Audience: audSvc,
	})

	configSvc := newTestConfigService(t, opts.configEntries)
	var audForService *audience.Service
	if !opts.audienceDisabled {
		audForService = audSvc
	}
	svc, err := NewService(ServiceConfig{
		Engine:          engine,
		FCap:            fcapSvc,
		Audience:        audForService,
		ConfigService:   configSvc,
		FCapTimeout:     50 * time.Millisecond,
		AudienceTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

type testServiceOptions struct {
	configEntries    []identityconfig.Entry
	cappedTuples     []capTuple
	memberships      []membershipFixture
	audienceDisabled bool
}

type capTuple struct {
	identity string
	seller   string
	pkg      string
}

type membershipFixture struct {
	userToken  string
	audienceID string
}

func TestService_EmptyEffectivePackages(t *testing.T) {
	svc := newTestService(t, testServiceOptions{
		configEntries: nil, // no configs → ResolveRequest returns empty
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "https://seller.example",
		PackageIDs:     []string{"pkg-1"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	result := svc.Evaluate(context.Background(), req)
	if len(result.Eligibility) != 0 {
		t.Fatalf("expected empty eligibility, got %v", result.Eligibility)
	}
}

func TestService_NoSegmentRules_OnlyFCapGates(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-1"}, TargetSegments: nil},
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-2"}, TargetSegments: nil},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		cappedTuples: []capTuple{
			{identity: "u1", seller: "s", pkg: "pkg-1"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "s",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	result := svc.Evaluate(context.Background(), req)
	got := eligibilityMap(result.Eligibility)
	if got["pkg-1"] {
		t.Errorf("pkg-1 should be capped, got eligible")
	}
	if !got["pkg-2"] {
		t.Errorf("pkg-2 should be eligible, got capped")
	}
}

func TestService_FCapPerIdentity(t *testing.T) {
	// User has two identities; either being capped on a package marks
	// that package ineligible.
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-1"}},
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-2"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		cappedTuples: []capTuple{
			{identity: "id5-token", seller: "s", pkg: "pkg-1"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "s",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities: []tmproto.IdentityToken{
			{UserToken: "maid-token", UIDType: tmproto.UIDTypeMAID},
			{UserToken: "id5-token", UIDType: tmproto.UIDTypeID5},
		},
	}
	result := svc.Evaluate(context.Background(), req)
	got := eligibilityMap(result.Eligibility)
	if got["pkg-1"] {
		t.Errorf("pkg-1: any identity capped → ineligible, got eligible")
	}
	if !got["pkg-2"] {
		t.Errorf("pkg-2: no identity capped → eligible, got ineligible")
	}
}

func TestService_AudienceFiltersByRule(t *testing.T) {
	rule := &targeting.SegmentRule{AllOf: []string{"seg-a"}}
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-1"}, TargetSegments: rule},
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-2"}, TargetSegments: nil}, // no rule
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		memberships: []membershipFixture{
			{userToken: "u1", audienceID: "seg-a"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "s",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	result := svc.Evaluate(context.Background(), req)
	got := eligibilityMap(result.Eligibility)
	if !got["pkg-1"] {
		t.Errorf("pkg-1: user in seg-a → eligible, got ineligible")
	}
	if !got["pkg-2"] {
		t.Errorf("pkg-2: no rule → eligible regardless, got ineligible")
	}

	// Same request, different user not in seg-a → pkg-1 ineligible, pkg-2 still eligible.
	req.Identities = []tmproto.IdentityToken{{UserToken: "stranger", UIDType: tmproto.UIDTypeID5}}
	result = svc.Evaluate(context.Background(), req)
	got = eligibilityMap(result.Eligibility)
	if got["pkg-1"] {
		t.Errorf("pkg-1: stranger not in seg-a → ineligible, got eligible")
	}
	if !got["pkg-2"] {
		t.Errorf("pkg-2: no rule → still eligible")
	}
}

func TestService_AudienceUnconfigured_RulesMarkIneligible(t *testing.T) {
	rule := &targeting.SegmentRule{AllOf: []string{"seg-a"}}
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-1"}, TargetSegments: rule},
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-2"}, TargetSegments: nil},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries:    entries,
		audienceDisabled: true,
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "s",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	result := svc.Evaluate(context.Background(), req)
	got := eligibilityMap(result.Eligibility)
	if got["pkg-1"] {
		t.Errorf("pkg-1: audience unconfigured + non-empty rule → ineligible (fail-closed)")
	}
	if !got["pkg-2"] {
		t.Errorf("pkg-2: no rule → eligible")
	}
}

func TestService_FCapTimeout_FailClosed(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "s", PackageID: "pkg-1"}},
	}
	// Wrap the in-memory fcap.Store in a delay shim that exceeds the
	// 1ms FCapTimeout we set below.
	fcapStore := &slowFCapStore{inner: fcap.NewMockStore(), delay: 50 * time.Millisecond}
	configSvc := newTestConfigService(t, entries)
	engine := targeting.NewIdentityEngine(targeting.IdentityEngineConfig{})
	svc, err := NewService(ServiceConfig{
		Engine:          engine,
		FCap:            fcap.New(fcapStore),
		ConfigService:   configSvc,
		FCapTimeout:     1 * time.Millisecond,
		AudienceTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "s",
		PackageIDs:     []string{"pkg-1"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	result := svc.Evaluate(context.Background(), req)
	got := eligibilityMap(result.Eligibility)
	if got["pkg-1"] {
		t.Fatalf("fcap timeout must fail closed; pkg-1 was eligible")
	}
}

// slowFCapStore is an fcap.Store wrapper that injects a fixed delay before
// every Field lookup. Used to drive the fcap-timeout test deterministically.
type slowFCapStore struct {
	inner *fcap.MockStore
	delay time.Duration
}

func (s *slowFCapStore) SetFields(ctx context.Context, key string, fields map[string]string, expireAt time.Time) error {
	return s.inner.SetFields(ctx, key, fields, expireAt)
}

func (s *slowFCapStore) SetFieldsBatch(ctx context.Context, batches []fcap.FieldsBatch) error {
	return s.inner.SetFieldsBatch(ctx, batches)
}

func (s *slowFCapStore) FieldExists(ctx context.Context, key, field string) (bool, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return s.inner.FieldExists(ctx, key, field)
}

func (s *slowFCapStore) FieldExistsBatch(ctx context.Context, lookups []fcap.FieldLookup) ([]bool, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.inner.FieldExistsBatch(ctx, lookups)
}

// failClosedSlowFCapStoreCompile is a compile-time assertion that the slow
// shim fully satisfies fcap.Store.
var _ fcap.Store = (*slowFCapStore)(nil)

func eligibilityMap(items []tmproto.PackageEligibility) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, e := range items {
		out[e.PackageID] = e.Eligible
	}
	return out
}

// ensure errors package is used to prevent unused-import drift if test edits
// remove references.
var _ = errors.New
