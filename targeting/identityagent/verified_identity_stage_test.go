package identityagent

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// mockVerifier records call count and returns a fixed result (scoped to the
// receiver's expected RP so the stage's defense-in-depth RP check passes) or
// a fixed error.
type mockVerifier struct {
	nullifier string
	claims    []tmproto.AttestationClaim
	err       error
	calls     int
}

func (m *mockVerifier) Verify(_ context.Context, _ *tmproto.Attestation, vctx targeting.VerifyContext) (targeting.VerifiedIdentity, error) {
	m.calls++
	if m.err != nil {
		return targeting.VerifiedIdentity{}, m.err
	}
	return targeting.VerifiedIdentity{
		Nullifier:      m.nullifier,
		RelyingPartyID: vctx.ExpectedRelyingPartyID,
		Claims:         targeting.NormalizeClaims(m.claims),
	}, nil
}

// staticAgeResolver maps pkgID → required age claim. Geo-independent.
type staticAgeResolver map[string]tmproto.AttestationClaim

func (r staticAgeResolver) ResolveRequiredAge(_ context.Context, pkgID, _ string) (tmproto.AttestationClaim, bool) {
	c, ok := r[pkgID]
	return c, ok
}

func newRecipient(t *testing.T) (*ecdh.PrivateKey, map[string]RecipientKey) {
	t.Helper()
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return sk, map[string]RecipientKey{"kid-1": {PrivateKey: sk, RelyingPartyID: "rp-1"}}
}

func validAtt(claims ...tmproto.AttestationClaim) tmproto.Attestation {
	return tmproto.Attestation{
		Issuer:         map[string]any{"domain": "world.org"},
		Scheme:         "world_id_v4",
		RelyingPartyID: "rp-1",
		SignalBinding:  "binding-committing-to-r1",
		Claims:         claims,
		Proof:          map[string]any{"merkle_root": "0x1"},
	}
}

func sealedCred(t *testing.T, kid string, pub *ecdh.PublicKey, att tmproto.Attestation) tmproto.SealedCredential {
	t.Helper()
	pt, err := json.Marshal(att)
	require.NoError(t, err)
	wire, err := tmproto.SealTmpx(tmproto.TmpxRecipient{Kid: kid, PublicKey: pub}, nil, pt)
	require.NoError(t, err)
	return tmproto.SealedCredential{AudienceKID: kid, Payload: wire}
}

func vidEntries() []identityconfig.Entry {
	return []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-alcohol"}},
		{Key: identityconfig.Key{SellerAgentURL: "seller.com", PackageID: "pkg-general"}},
	}
}

func vidReq(sealed ...tmproto.SealedCredential) *tmproto.IdentityMatchRequest {
	return &tmproto.IdentityMatchRequest{
		RequestID:         "r1",
		SellerAgentURL:    "seller.com",
		PackageIDs:        []string{"pkg-alcohol", "pkg-general"},
		Identities:        []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
		Country:           "US",
		SealedCredentials: sealed,
	}
}

// FAIL-CLOSED DEFAULT: with NO verifier wired, a correctly-sealed attestation
// asserting age_over_21 is treated as absent — the age-gated package is
// ineligible. Proves trust-without-verify is unreachable by configuration
// omission.
func TestService_VerifiedIdentity_FailClosedDefault(t *testing.T) {
	sk, _ := newRecipient(t)
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
		// verifier and recipientKeys deliberately unset.
	})
	req := vidReq(sealedCred(t, "kid-1", sk.PublicKey(), validAtt(tmproto.AttestationClaimAgeOver21)))
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-alcohol"], "no verifier ⇒ attestation absent ⇒ age-gated package ineligible")
	assert.True(t, got["pkg-general"], "ungated package stays eligible")
}

// HAPPY PATH: a wired verifier returns age_over_21; the age-gated package is
// eligible.
func TestService_VerifiedIdentity_HappyPath(t *testing.T) {
	sk, keys := newRecipient(t)
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman, tmproto.AttestationClaimAgeOver21}}
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	req := vidReq(sealedCred(t, "kid-1", sk.PublicKey(), validAtt(tmproto.AttestationClaimAgeOver21)))
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.True(t, got["pkg-alcohol"], "verified age_over_21 satisfies the 21 gate")
	assert.True(t, got["pkg-general"], "ungated package eligible")
	assert.Equal(t, 1, mv.calls)
}

// AGE GATE: verified claim is only age_over_18; the age_over_21 package is
// ineligible while the ungated package stays eligible.
func TestService_VerifiedIdentity_AgeGateBelowThreshold(t *testing.T) {
	sk, keys := newRecipient(t)
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimAgeOver18}}
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	req := vidReq(sealedCred(t, "kid-1", sk.PublicKey(), validAtt(tmproto.AttestationClaimAgeOver18)))
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-alcohol"], "age_over_18 does not satisfy the 21 gate")
	assert.True(t, got["pkg-general"], "ungated package eligible")
}

// FCAP ON NULLIFIER: a cap recorded on the relying-party-namespaced nullifier
// key caps the package; a cap on the raw user token would NOT (proves fcap
// keys on the verified nullifier, namespaced, not the UserToken).
func TestService_VerifiedIdentity_FcapOnNullifier(t *testing.T) {
	sk, keys := newRecipient(t)
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	capKey := targeting.VerifiedIdentity{Nullifier: "N", RelyingPartyID: "rp-1"}.CapKey()
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		// No age gate — isolate fcap behavior. Cap the namespaced nullifier
		// key for pkg-alcohol, and (decoy) cap the raw user token too.
		cappedTuples: []capTuple{
			{identity: capKey, seller: "seller.com", pkg: "pkg-alcohol"},
			{identity: "u1", seller: "seller.com", pkg: "pkg-general"},
		},
	})
	req := vidReq(sealedCred(t, "kid-1", sk.PublicKey(), validAtt(tmproto.AttestationClaimUniqueHuman)))
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-alcohol"], "capped on the namespaced nullifier key ⇒ ineligible")
	assert.True(t, got["pkg-general"], "cap on the raw UserToken does not apply — fcap keys on the verified nullifier")
}

// RP BINDING (blocker): a sealed attestation minted for a different relying
// party than this receiver acts as is dropped SDK-side BEFORE the verifier is
// called — a forwarded/replayed proof cannot be trusted.
func TestService_VerifiedIdentity_RPBindingMismatch(t *testing.T) {
	sk, keys := newRecipient(t) // recipient acts as rp-1
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimAgeOver21}}
	att := validAtt(tmproto.AttestationClaimAgeOver21)
	att.RelyingPartyID = "rp-EVIL" // minted for another RP
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	req := vidReq(sealedCred(t, "kid-1", sk.PublicKey(), att))
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-alcohol"], "RP mismatch ⇒ attestation absent ⇒ age-gated package ineligible")
	assert.Equal(t, 0, mv.calls, "verifier MUST NOT be called when relying_party_id != the RP we act as")
}

// MANDATORY SIGNAL_BINDING (blocker): an attestation with no signal_binding is
// dropped pre-verify (fail-closed replay defense), verifier never called.
func TestService_VerifiedIdentity_MissingSignalBinding(t *testing.T) {
	sk, keys := newRecipient(t)
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimAgeOver21}}
	att := validAtt(tmproto.AttestationClaimAgeOver21)
	att.SignalBinding = "" // omitted
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	req := vidReq(sealedCred(t, "kid-1", sk.PublicKey(), att))
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-alcohol"], "empty signal_binding ⇒ absent ⇒ ineligible")
	assert.Equal(t, 0, mv.calls, "verifier MUST NOT be called for an attestation with no signal_binding")
}

// VERIFIER ERROR: a verifier that errors yields no verified identity — the
// age-gated package is ineligible (treated as absent, not asserted-true).
func TestService_VerifiedIdentity_VerifierError(t *testing.T) {
	sk, keys := newRecipient(t)
	mv := &mockVerifier{err: assertErr("verifier down")}
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	req := vidReq(sealedCred(t, "kid-1", sk.PublicKey(), validAtt(tmproto.AttestationClaimAgeOver21)))
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.False(t, got["pkg-alcohol"], "verifier error ⇒ absent ⇒ ineligible")
}

// MULTI-AUDIENCE: two sealed credentials, one addressed to an audience_kid we
// hold a key for and one we do not. Only the held entry is opened+verified;
// the other is silently ignored (no error, no eligibility effect).
func TestService_VerifiedIdentity_MultiAudienceOnlyHeldKey(t *testing.T) {
	sk, keys := newRecipient(t) // we hold "kid-1"
	other, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimAgeOver21}}
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	req := vidReq(
		sealedCred(t, "kid-2", other.PublicKey(), validAtt(tmproto.AttestationClaimAgeOver21)),
		sealedCred(t, "kid-1", sk.PublicKey(), validAtt(tmproto.AttestationClaimAgeOver21)),
	)
	got := eligibilityMap(svc.Evaluate(t.Context(), req).Eligibility)
	assert.True(t, got["pkg-alcohol"], "the held-key credential verifies; the other audience's entry is ignored")
	assert.Equal(t, 1, mv.calls, "only the credential for an audience we hold a key for reaches the verifier")
}

// RequiresVerifiedIdentity fail-closed gate logic (unit-level, since the
// config snapshot does not yet populate the field — see PackageIdentityConfig).
func TestService_computeVerifiedIdentityGate_RequiresVerifiedIdentity(t *testing.T) {
	svc := &Service{} // gate uses only resolved config + the verified slice
	resolved := &targeting.ResolvedPackages{IdentityConfigs: map[string]*targeting.PackageIdentityConfig{
		"pkg-strict": {RequiresVerifiedIdentity: true},
		"pkg-open":   {},
	}}
	req := &tmproto.IdentityMatchRequest{RequestID: "r1"}

	// No verified identity present ⇒ the strict package is rejected, the open
	// one is not.
	rejected := svc.computeVerifiedIdentityGate(t.Context(), req, resolved, []string{"pkg-strict", "pkg-open"}, nil)
	_, strictRejected := rejected["pkg-strict"]
	_, openRejected := rejected["pkg-open"]
	assert.True(t, strictRejected, "RequiresVerifiedIdentity + no verified identity ⇒ rejected (fail-closed)")
	assert.False(t, openRejected, "package without the requirement is unaffected")

	// A verified identity present ⇒ the strict package clears.
	withVID := []targeting.VerifiedIdentity{{Nullifier: "N", RelyingPartyID: "rp-1"}}
	rejected = svc.computeVerifiedIdentityGate(t.Context(), req, resolved, []string{"pkg-strict"}, withVID)
	_, strictRejected = rejected["pkg-strict"]
	assert.False(t, strictRejected, "RequiresVerifiedIdentity satisfied by a present verified identity")
}

// DoS: more than maxSealedCredentials sealed entries are truncated BEFORE any
// crypto — the verifier is called at most maxSealedCredentials times, so a
// flood cannot force unbounded HPKE opens + verifier round-trips.
func TestService_VerifiedIdentity_BoundsSealedCount(t *testing.T) {
	sk, keys := newRecipient(t)
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimAgeOver21}}
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	creds := make([]tmproto.SealedCredential, 0, maxSealedCredentials+5)
	for i := 0; i < maxSealedCredentials+5; i++ {
		creds = append(creds, sealedCred(t, "kid-1", sk.PublicKey(), validAtt(tmproto.AttestationClaimAgeOver21)))
	}
	svc.Evaluate(t.Context(), vidReq(creds...))
	assert.LessOrEqual(t, mv.calls, maxSealedCredentials,
		"sealed_credentials count must be bounded before any crypto/verifier call")
}

// DoS: a sealed entry whose payload exceeds maxSealedPayloadBytes is dropped at
// the size check, before any HPKE open or verifier call.
func TestService_VerifiedIdentity_DropsOversizedPayload(t *testing.T) {
	_, keys := newRecipient(t)
	mv := &mockVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimAgeOver21}}
	svc := newTestService(t, testServiceOptions{
		configEntries: vidEntries(),
		verifier:      mv,
		recipientKeys: keys,
		ageResolver:   staticAgeResolver{"pkg-alcohol": tmproto.AttestationClaimAgeOver21},
	})
	oversized := tmproto.SealedCredential{AudienceKID: "kid-1", Payload: strings.Repeat("A", maxSealedPayloadBytes+1)}
	got := eligibilityMap(svc.Evaluate(t.Context(), vidReq(oversized)).Eligibility)
	assert.False(t, got["pkg-alcohol"], "oversized payload ⇒ dropped ⇒ attestation absent ⇒ ineligible")
	assert.Equal(t, 0, mv.calls, "an oversized payload must be dropped before any HPKE open or verifier call")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
