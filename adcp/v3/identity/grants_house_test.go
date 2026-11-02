package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
	"github.com/stretchr/testify/require"
)

// Implicit authority extends to the brand's house (L2 Grants in trust
// records, adcp#8169): when the account operator and the signer's operator
// share a registrable domain with the brand's domain or its house's, no
// grant is needed.

const houseAgent = "https://buy.house-7k3q.com"

// brandJSON models brand.json for a mutual-assertion resolver: a leaf names
// its house with house_domain, and the edge is verified only when the
// house's brand_refs lists the leaf back (with its id in the portfolio).
var brandJSON = map[string]struct {
	houseDomain string
	brandRefs   map[string]string
}{
	"novamotors-7k3q.com": {houseDomain: "house-7k3q.com"},
	"orphan-7k3q.com":     {houseDomain: "house-7k3q.com"},
	"house-7k3q.com":      {brandRefs: map[string]string{"novamotors-7k3q.com": "nova_motors"}},
}

func mutualAssertionHouse(_ context.Context, brandDomain, _ string) (*BrandHouse, error) {
	house := brandJSON[brandDomain].houseDomain
	if house == "" {
		return nil, nil
	}
	id, ok := brandJSON[house].brandRefs[brandDomain]
	if !ok {
		// Leaf-only: the house does not list the brand back, so the
		// relationship is unverified and the brand is its own house.
		return nil, nil
	}
	return &BrandHouse{Domain: house, BrandID: id}, nil
}

func houseRecords(houseGrants string) map[string]string {
	if houseGrants == "" {
		houseGrants = "[]"
	}
	return with(baseRecords, map[string]string{
		"house-7k3q.com":      `{"agents":[{"url":"https://buy.house-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":` + houseGrants + `}`,
		"novamotors-7k3q.com": `{"grants":[]}`,
		"orphan-7k3q.com":     `{"grants":[]}`,
	})
}

func TestHouseOperatingItsOwnChildBrandNeedsNoGrant(t *testing.T) {
	for _, profile := range []string{signing.ProfileTagWebBotAuth, signing.ProfileTagRequestSigning} {
		t.Run(profile, func(t *testing.T) {
			r := grantRig(t, houseRecords(""))
			g := &GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse}
			res, err := g.Check(context.Background(), GrantRequest{AgentOrigin: houseAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "house-7k3q.com", SigningProfile: profile})
			require.NoError(t, err)
			require.True(t, res.Authorized, res.Reason)
			require.False(t, res.ExpiresAt.IsZero())
			require.Zero(t, r.server.count(trustURL("novamotors-7k3q.com")), "only the house's record is read")
		})
	}
}

func TestHouseThatDoesNotListTheBrandBackNeedsAGrant(t *testing.T) {
	ctx := context.Background()
	req := GrantRequest{AgentOrigin: houseAgent, BrandDomain: "orphan-7k3q.com", AccountOperator: "house-7k3q.com"}
	r := grantRig(t, houseRecords(""))
	res, _ := (&GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse}).Check(ctx, req)
	require.False(t, res.Authorized)
	require.Equal(t, GrantReasonAbsent, res.Reason)
	code, details := permissionDetails(t, res.Err())
	require.Equal(t, "PERMISSION_DENIED", code)
	require.Equal(t, "grant_absent", details["reason"])

	// The brand is its own house, so its own record can grant the claimed house.
	r = grantRig(t, with(houseRecords(""), map[string]string{"orphan-7k3q.com": `{"grants":[{"grantee":"house-7k3q.com","scopes":["adcp:media_buying"]}]}`}))
	res, _ = (&GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse}).Check(ctx, req)
	require.True(t, res.Authorized, res.Reason)
}

func TestOperatorOutsideTheBrandAndItsHouseStillNeedsAGrant(t *testing.T) {
	ctx := context.Background()
	r := grantRig(t, houseRecords(""))
	g := &GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse}
	res, _ := g.Check(ctx, GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "agency-7k3q.com"})
	require.False(t, res.Authorized)
	require.Equal(t, GrantReasonAbsent, res.Reason)
	// The house's own agent under another organization's account is not implicit.
	res, _ = g.Check(ctx, GrantRequest{AgentOrigin: houseAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "agency-7k3q.com"})
	require.False(t, res.Authorized)
	require.Equal(t, GrantReasonAbsent, res.Reason)

	// A house grant to the agency, checked against the house's portfolio id.
	r = grantRig(t, houseRecords(`[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"],"profiles":{"adcp":{"brands":["nova_motors"]}}}]`))
	res, _ = (&GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse}).Check(ctx, GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "own_id"})
	require.True(t, res.Authorized, res.Reason)
	require.Zero(t, r.server.count(trustURL("novamotors-7k3q.com")), "grants in the brand's own record are not consulted once it has a house")
}

func TestHouseResolutionFailureIsNeverGrantAbsent(t *testing.T) {
	broken := func(context.Context, string, string) (*BrandHouse, error) {
		return nil, errors.New("brand.json unreachable")
	}
	for _, nonSpend := range []bool{false, true} {
		sellerCalled := false
		seller := func(context.Context, GrantRequest) (bool, error) { sellerCalled = true; return true, nil }
		r := grantRig(t, houseRecords(""))
		g := &GrantEvaluator{Records: r.eval, ResolveHouse: broken, SellerAuthorization: seller}
		res, err := g.Check(context.Background(), GrantRequest{AgentOrigin: houseAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "house-7k3q.com", NonSpend: nonSpend})
		require.NoError(t, err)
		require.False(t, res.Authorized)
		require.True(t, res.Unavailable)
		require.Equal(t, GrantReasonHouseUnavailable, res.Reason)
		code, _ := permissionDetails(t, res.Err())
		require.Equal(t, "SERVICE_UNAVAILABLE", code)
		require.False(t, sellerCalled)
	}
}

func TestImplicitHouseAuthorityComparesRegistrableDomains(t *testing.T) {
	cases := []struct {
		name, agent, account, house string
		records                     map[string]string
		implicit                    bool
	}{
		{
			// Hosts reduce to their registrable domain on every side.
			name: "subdomains", agent: "https://buy.house-7k3q.com", account: "ads.house-7k3q.com", house: "www.house-7k3q.com",
			records:  map[string]string{"house-7k3q.com": `{"agents":[{"url":"https://buy.house-7k3q.com","roles":["adcp:buying"]}]}`},
			implicit: true,
		},
		{
			name: "multi-label suffix", agent: "https://agent.buy.house-7k3q.co.uk", account: "house-7k3q.co.uk", house: "brands.house-7k3q.co.uk",
			records:  map[string]string{"house-7k3q.co.uk": `{"agents":[{"url":"https://agent.buy.house-7k3q.co.uk","roles":["adcp:buying"]}]}`},
			implicit: true,
		},
		{
			// PSL PRIVATE section: two github.io tenants are different operators.
			name: "private suffix", agent: "https://alice-7k3q.github.io", account: "alice-7k3q.github.io", house: "bob-7k3q.github.io",
			records: map[string]string{
				"alice-7k3q.github.io": `{"agents":[{"url":"https://alice-7k3q.github.io","roles":["adcp:buying"]}]}`,
				"bob-7k3q.github.io":   `{"grants":[]}`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolve := func(context.Context, string, string) (*BrandHouse, error) {
				return &BrandHouse{Domain: tc.house}, nil
			}
			r := grantRig(t, tc.records)
			res, err := (&GrantEvaluator{Records: r.eval, ResolveHouse: resolve}).Check(context.Background(), GrantRequest{AgentOrigin: tc.agent, BrandDomain: "novamotors-7k3q.com", AccountOperator: tc.account})
			require.NoError(t, err)
			require.Equal(t, tc.implicit, res.Authorized, res.Reason)
			if !tc.implicit {
				require.Equal(t, GrantReasonAbsent, res.Reason)
			}
		})
	}
}

func TestBrandImplicitAuthorityNeverWaitsOnTheHouse(t *testing.T) {
	ctx := context.Background()
	const brandAgent = "https://legacy.novamotors-7k3q.com"
	records := map[string]string{"novamotors-7k3q.com": `{"agents":[{"url":"https://legacy.novamotors-7k3q.com/mcp","roles":["adcp:buying"]}]}`}
	resolvers := map[string]HouseResolver{
		"failing resolver": func(context.Context, string, string) (*BrandHouse, error) {
			return nil, errors.New("brand.json unreachable")
		},
		"house without a record": func(context.Context, string, string) (*BrandHouse, error) {
			return &BrandHouse{Domain: "quiet-7k3q.com"}, nil
		},
	}
	for name, resolve := range resolvers {
		for _, profile := range []string{signing.ProfileTagWebBotAuth, signing.ProfileTagRequestSigning} {
			t.Run(name+"/"+profile, func(t *testing.T) {
				r := grantRig(t, records)
				res, err := (&GrantEvaluator{Records: r.eval, ResolveHouse: resolve}).Check(ctx, GrantRequest{AgentOrigin: brandAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "novamotors-7k3q.com", SigningProfile: profile})
				require.NoError(t, err)
				require.True(t, res.Authorized, res.Reason)
				require.Zero(t, r.server.count(trustURL("quiet-7k3q.com")))
			})
		}
	}
}

func TestResolvedHouseRecordIsTheOnlySourceOfGrants(t *testing.T) {
	ctx := context.Background()
	claim := GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "agency-7k3q.com", BrandID: "own_id"}

	t.Run("exclusive_grants is read from the house's record", func(t *testing.T) {
		seller := func(context.Context, GrantRequest) (bool, error) { return true, nil }
		r := grantRig(t, with(houseRecords(`[{"grantee":"other-7k3q.com","scopes":["adcp:media_buying"]}]`), map[string]string{
			"house-7k3q.com": `{"agents":[{"url":"https://buy.house-7k3q.com/mcp","roles":["adcp:buying"]}],"exclusive_grants":true,"grants":[{"grantee":"other-7k3q.com","scopes":["adcp:media_buying"]}]}`,
		}))
		res, _ := (&GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse, SellerAuthorization: seller}).Check(ctx, claim)
		require.False(t, res.Authorized, "the house's exclusive grants disable seller discretion")

		r = grantRig(t, with(houseRecords(""), map[string]string{
			"novamotors-7k3q.com": `{"exclusive_grants":true,"grants":[{"grantee":"other-7k3q.com","scopes":["adcp:media_buying"]}]}`,
		}))
		res, _ = (&GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse, SellerAuthorization: seller}).Check(ctx, claim)
		require.True(t, res.Authorized, "the brand's own exclusive flag is not consulted once it has a house")
	})

	t.Run("hooks see the claim, not the house's brand id", func(t *testing.T) {
		var seen GrantRequest
		seller := func(_ context.Context, req GrantRequest) (bool, error) { seen = req; return true, nil }
		r := grantRig(t, houseRecords(""))
		res, _ := (&GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse, SellerAuthorization: seller}).Check(ctx, claim)
		require.True(t, res.Authorized)
		require.Equal(t, "own_id", seen.BrandID)
		require.Equal(t, "novamotors-7k3q.com", seen.BrandDomain)
	})

	t.Run("legacy authorization runs while the house has never published", func(t *testing.T) {
		var seen GrantRequest
		legacy := func(_ context.Context, req GrantRequest) (bool, error) { seen = req; return true, nil }
		quietHouse := func(context.Context, string, string) (*BrandHouse, error) {
			return &BrandHouse{Domain: "quiet-7k3q.com", BrandID: "nova_motors"}, nil
		}
		r := grantRig(t, houseRecords(""))
		res, _ := (&GrantEvaluator{Records: r.eval, ResolveHouse: quietHouse, LegacyAuthorization: legacy}).Check(ctx, claim)
		require.True(t, res.Authorized)
		require.True(t, res.LegacyFallbackAllowed)
		require.Equal(t, "own_id", seen.BrandID)
		require.Zero(t, r.server.count(trustURL("novamotors-7k3q.com")), "the brand's own record is not read")
	})

	t.Run("a two-link chain matches the house's brand id on both links", func(t *testing.T) {
		r := grantRig(t, with(houseRecords(`[{"grantee":"agency-7k3q.com","scopes":["adcp:media_buying"],"profiles":{"adcp":{"brands":["nova_motors"]}}}]`), map[string]string{
			"agency-7k3q.com": `{"agents":[{"url":"https://buy.agency-7k3q.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"platform-7k3q.com","scopes":["adcp:media_buying"],"profiles":{"adcp":{"brands":["nova_motors"]}}}]}`,
		}))
		chain := claim
		chain.AgentOrigin = platformAgent
		res, _ := (&GrantEvaluator{Records: r.eval, ResolveHouse: mutualAssertionHouse}).Check(ctx, chain)
		require.True(t, res.Authorized, res.Reason)
	})
}

func TestHouseWithoutARegistrableDomainIsAResolverFailure(t *testing.T) {
	resolve := func(context.Context, string, string) (*BrandHouse, error) {
		return &BrandHouse{Domain: "github.io"}, nil
	}
	r := grantRig(t, houseRecords(""))
	res, _ := (&GrantEvaluator{Records: r.eval, ResolveHouse: resolve}).Check(context.Background(), GrantRequest{AgentOrigin: agencyAgent, BrandDomain: "novamotors-7k3q.com", AccountOperator: "agency-7k3q.com"})
	require.False(t, res.Authorized)
	require.True(t, res.Unavailable)
	require.Equal(t, GrantReasonHouseUnavailable, res.Reason)
}
