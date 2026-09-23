package identityagent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestService_FCap_AllUndecodable_FailClosed pins the primary I1
// bypass fix: when the wire request carried identities but zero of
// them canonicalized (e.g. only pairid was sent and this deployment
// has no pairid decoder configured), TMP invariant #2 cannot be
// verified — the fcap stage MUST fail closed regardless of what the
// store says. Before the fix, the fcap slice was empty and IsCappedAny
// returned all-false, silently serving capped users on their next
// request.
func TestService_FCap_AllUndecodable_FailClosed(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-1"}},
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-2"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities: []tmproto.IdentityToken{
			{UserToken: "opaque", UIDType: tmproto.UIDTypePairID},
		},
	}
	// WireCount 1, SuccessCount 0 → fail-closed on both packages.
	result := svc.EvaluateWithDecode(t.Context(), req, DecodeSummary{WireCount: 1, SuccessCount: 0})
	got := eligibilityMap(result.Eligibility)
	assert.False(t, got["pkg-1"], "pkg-1 must be ineligible when the request's only identity was undecodable")
	assert.False(t, got["pkg-2"], "pkg-2 must be ineligible when the request's only identity was undecodable")
}

// TestService_FCap_PartialDecode_PermissivePasses pins the permissive
// default behavior: when at least one identity decoded, the fcap stage
// runs against that identity as before. Deployments that don't ship
// every decoder don't get over-blocked when some identities decoded
// successfully.
func TestService_FCap_PartialDecode_PermissivePasses(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-1"}},
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-2"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
		cappedTuples: []capTuple{
			{identity: "id5-token", seller: "https://seller.example.com/agent", pkg: "pkg-1"},
		},
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities: []tmproto.IdentityToken{
			{UserToken: "id5-token", UIDType: tmproto.UIDTypeID5},
		},
	}
	// Wire had 2, we decoded 1 (id5). Permissive mode uses the decoded
	// one; pkg-1 is capped for it, pkg-2 is not.
	result := svc.EvaluateWithDecode(t.Context(), req, DecodeSummary{WireCount: 2, SuccessCount: 1})
	got := eligibilityMap(result.Eligibility)
	assert.False(t, got["pkg-1"], "capped identity must gate pkg-1")
	assert.True(t, got["pkg-2"], "permissive mode must let pkg-2 through when at least one identity decoded")
}

// TestService_FCap_PartialDecode_StrictFailsClosed pins the opt-in
// strict mode: regulated deployments that need the letter of TMP
// invariant #2 fail closed as soon as ANY identity could not be
// checked against cap-state.
func TestService_FCap_PartialDecode_StrictFailsClosed(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-1"}},
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-2"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries:       entries,
		strictOnUndecodable: true,
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-1", "pkg-2"},
		Identities: []tmproto.IdentityToken{
			{UserToken: "id5-token", UIDType: tmproto.UIDTypeID5},
		},
	}
	result := svc.EvaluateWithDecode(t.Context(), req, DecodeSummary{WireCount: 2, SuccessCount: 1})
	got := eligibilityMap(result.Eligibility)
	assert.False(t, got["pkg-1"], "strict mode must fail closed on pkg-1 when any identity was undecodable")
	assert.False(t, got["pkg-2"], "strict mode must fail closed on pkg-2 when any identity was undecodable")
}

// TestService_FCap_FullDecode_StrictPasses confirms strict mode does
// NOT over-block when every identity decoded — the fcap stage runs
// normally.
func TestService_FCap_FullDecode_StrictPasses(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-1"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries:       entries,
		strictOnUndecodable: true,
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-1"},
		Identities: []tmproto.IdentityToken{
			{UserToken: "id5-token", UIDType: tmproto.UIDTypeID5},
		},
	}
	result := svc.EvaluateWithDecode(t.Context(), req, DecodeSummary{WireCount: 1, SuccessCount: 1})
	got := eligibilityMap(result.Eligibility)
	assert.True(t, got["pkg-1"], "strict mode with full decode and no caps must serve normally")
}

// TestService_Evaluate_ZeroSummaryPreservesBackCompat pins that
// callers using the classic Evaluate method (or EvaluateWithDecode
// with a zero DecodeSummary) still see pre-I1 behavior — the
// fail-closed policy is not triggered, matching what direct-consumer
// SDKs expected.
func TestService_Evaluate_ZeroSummaryPreservesBackCompat(t *testing.T) {
	entries := []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-1"}},
	}
	svc := newTestService(t, testServiceOptions{
		configEntries: entries,
	})
	req := &tmproto.IdentityMatchRequest{
		RequestID:      "r1",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-1"},
		Identities: []tmproto.IdentityToken{
			{UserToken: "id5-token", UIDType: tmproto.UIDTypeID5},
		},
	}
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.True(t, got["pkg-1"], "Evaluate (zero summary) must not trigger fail-closed")
}
