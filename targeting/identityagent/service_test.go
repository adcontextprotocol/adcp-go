package identityagent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, err)
	require.NoError(t, svc.Start(t.Context()))
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
			require.NoError(t, fcapSvc.RecordCap(t.Context(), tup.identity,
				[]fcap.Field{{SellerAgentURL: tup.seller, PackageID: tup.pkg}},
				expireAt))
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
		require.NoError(t, audSvc.UpsertBatch(t.Context(), upserts))
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
	require.NoError(t, err)
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
	result := svc.Evaluate(t.Context(), req)
	assert.Empty(t, result.Eligibility)
}

func TestService_NoSegmentRules_OnlyFCapGates(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-1"}, TargetSegments: nil},
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-2"}, TargetSegments: nil},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		cappedTuples: []capTuple{
			{identity: "u1", seller: "seller.com", pkg: "pkg-1"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "seller.com",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-1"], "pkg-1 should be capped")
	assert.True(t, got["pkg-2"], "pkg-2 should be eligible")
}

func TestService_FCapPerIdentity(t *testing.T) {
	// User has two identities; either being capped on a package marks
	// that package ineligible.
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-1"}},
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-2"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		cappedTuples: []capTuple{
			{identity: "id5-token", seller: "seller.com", pkg: "pkg-1"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "seller.com",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities: []tmproto.IdentityToken{
			{UserToken: "maid-token", UIDType: tmproto.UIDTypeMAID},
			{UserToken: "id5-token", UIDType: tmproto.UIDTypeID5},
		},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-1"], "any identity capped → ineligible")
	assert.True(t, got["pkg-2"], "no identity capped → eligible")
}

func TestService_AudienceFiltersByRule(t *testing.T) {
	rule := &targeting.SegmentRule{AllOf: []string{"seg-a"}}
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-1"}, TargetSegments: rule},
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-2"}, TargetSegments: nil}, // no rule
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		memberships: []membershipFixture{
			{userToken: "u1", audienceID: "seg-a"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "seller.com",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.True(t, got["pkg-1"], "user in seg-a → eligible")
	assert.True(t, got["pkg-2"], "no rule → eligible regardless")

	// Same request, different user not in seg-a → pkg-1 ineligible, pkg-2 still eligible.
	req.Identities = []tmproto.IdentityToken{{UserToken: "stranger", UIDType: tmproto.UIDTypeID5}}
	got = eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-1"], "stranger not in seg-a → ineligible")
	assert.True(t, got["pkg-2"], "no rule → still eligible")
}

func TestService_AudienceUnconfigured_RulesMarkIneligible(t *testing.T) {
	rule := &targeting.SegmentRule{AllOf: []string{"seg-a"}}
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-1"}, TargetSegments: rule},
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-2"}, TargetSegments: nil},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries:    entries,
		audienceDisabled: true,
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "seller.com",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-1"], "audience unconfigured + non-empty rule → ineligible (fail-closed)")
	assert.True(t, got["pkg-2"], "no rule → eligible")
}

func TestService_FCapTimeout_FailClosed(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-1"}},
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
	require.NoError(t, err)
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "seller.com",
		PackageIDs:     []string{"pkg-1"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	require.False(t, got["pkg-1"], "fcap timeout must fail closed")
}

// TestService_FCap_NormalizesSellerURLToRegistrableDomain ensures the fcap
// stage reduces req.SellerAgentURL to its registrable domain (eTLD+1) before
// looking up markers — matching the transformation frequency-writer applies
// when it writes them. The cap is recorded under "seller.example.com" while
// the request carries the full "https://sub.seller.example.com/agent" form;
// the cap must still apply.
func TestService_FCap_NormalizesSellerURLToRegistrableDomain(t *testing.T) {
	// The full URL carries subdomain + path; its eTLD+1 is "seller.com".
	// frequency-writer would record the marker under the eTLD+1; the
	// fcap stage must reduce the request URL the same way for the
	// lookup to find it.
	fullURL := "https://api.seller.com/agent"
	registrable := "seller.com"
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: fullURL, PackageID: "pkg-1"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		cappedTuples: []capTuple{
			{identity: "u1", seller: registrable, pkg: "pkg-1"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: fullURL,
		PackageIDs:     []string{"pkg-1"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-1"], "cap recorded under registrable domain must apply to a request that uses a subdomain/path form")
}

// TestService_FCap_UnregistrableSellerURL_NoCapApplied verifies the symmetric
// behavior to frequency-writer's "skip on invalid URL": if the request's
// seller URL can't be reduced to a registrable domain, no marker exists, so
// the fcap stage applies no caps and leaves eligibility to other stages.
func TestService_FCap_UnregistrableSellerURL_NoCapApplied(t *testing.T) {
	// "http://localhost" parses as a URL but localhost has no eTLD+1, so
	// urlutil.Registrable returns ErrInvalid.
	sellerURL := "http://localhost"
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: sellerURL, PackageID: "pkg-1"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		// Seed a cap on the unreduced URL so we can prove the fcap stage
		// never consults it.
		cappedTuples: []capTuple{
			{identity: "u1", seller: sellerURL, pkg: "pkg-1"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: sellerURL,
		PackageIDs:     []string{"pkg-1"},
		Identities:     []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.True(t, got["pkg-1"], "unregistrable seller URL must skip fcap and leave the package eligible")
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
