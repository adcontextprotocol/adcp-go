package targeting_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// stubVerifier returns a fixed identity (scoped to the receiver's expected RP
// so the post-verify RP check passes, unless rpOverride is set) or a fixed
// error, and records how many times it was called.
type stubVerifier struct {
	nullifier  string
	claims     []tmproto.AttestationClaim
	rpOverride string
	err        error
	calls      int
}

func (s *stubVerifier) Verify(_ context.Context, _ *tmproto.Attestation, vctx targeting.VerifyContext) (targeting.VerifiedIdentity, error) {
	s.calls++
	if s.err != nil {
		return targeting.VerifiedIdentity{}, s.err
	}
	rp := vctx.ExpectedRelyingPartyID
	if s.rpOverride != "" {
		rp = s.rpOverride
	}
	return targeting.VerifiedIdentity{
		Nullifier:      s.nullifier,
		RelyingPartyID: rp,
		Claims:         targeting.NormalizeClaims(s.claims),
	}, nil
}

// recordingObserver captures the drop reasons and verifier-failure count.
type recordingObserver struct {
	drops         []string
	verifierFails int
}

func (o *recordingObserver) Dropped(_ context.Context, reason string) {
	o.drops = append(o.drops, reason)
}
func (o *recordingObserver) VerifierFailed(context.Context) { o.verifierFails++ }

func newKey(t *testing.T, rp string) (*ecdh.PrivateKey, map[string]targeting.RecipientKey) {
	t.Helper()
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return sk, map[string]targeting.RecipientKey{"kid-1": {PrivateKey: sk, RelyingPartyID: rp}}
}

func attestation(rp string, claims ...tmproto.AttestationClaim) tmproto.Attestation {
	return tmproto.Attestation{
		Issuer:         map[string]any{"domain": "world.org"},
		Scheme:         "world_id_v4",
		RelyingPartyID: rp,
		SignalBinding:  "binding-committing-to-r1",
		Claims:         claims,
		Proof:          map[string]any{"merkle_root": "0x1"},
	}
}

func sealTo(t *testing.T, kid string, pub *ecdh.PublicKey, att tmproto.Attestation) tmproto.SealedCredential {
	t.Helper()
	pt, err := json.Marshal(att)
	require.NoError(t, err)
	wire, err := tmproto.SealTmpx(tmproto.TmpxRecipient{Kid: kid, PublicKey: pub}, nil, pt)
	require.NoError(t, err)
	return tmproto.SealedCredential{AudienceKID: kid, Payload: wire}
}

func baseCtx() targeting.VerifyContext {
	return targeting.VerifyContext{RequestID: "r1", Country: "US", Now: time.Unix(1_700_000_000, 0)}
}

// Fail-closed by omission: with no verifier wired, OpenAndVerify returns nil and
// never opens anything, so a perfectly-sealed attestation is treated as absent.
func TestOpenAndVerify_NoVerifierIsAbsent(t *testing.T) {
	sk, keys := newKey(t, "rp-1")
	creds := []tmproto.SealedCredential{sealTo(t, "kid-1", sk.PublicKey(), attestation("rp-1", tmproto.AttestationClaimAgeOver21))}

	assert.Nil(t, targeting.OpenAndVerify(t.Context(), creds, keys, nil, baseCtx(), nil))
	assert.Nil(t, targeting.OpenAndVerify(t.Context(), creds, nil, &stubVerifier{}, baseCtx(), nil))
	assert.Nil(t, targeting.OpenAndVerify(t.Context(), nil, keys, &stubVerifier{}, baseCtx(), nil))
}

// Verify-before-trust: claims come from the VERIFIER, never the asserted
// attestation. An attestation asserting age_over_21 yields only the claims the
// verifier confirms (unique_human) — the age claim does not leak through.
func TestOpenAndVerify_TrustsVerifierNotAttestation(t *testing.T) {
	sk, keys := newKey(t, "rp-1")
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	creds := []tmproto.SealedCredential{sealTo(t, "kid-1", sk.PublicKey(), attestation("rp-1", tmproto.AttestationClaimAgeOver21))}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), nil)
	require.Len(t, got, 1)
	assert.Equal(t, "rp-1", got[0].RelyingPartyID)
	assert.True(t, got[0].ClaimsSatisfy(tmproto.AttestationClaimUniqueHuman))
	assert.False(t, got[0].ClaimsSatisfy(tmproto.AttestationClaimAgeOver21),
		"the asserted age_over_21 must NOT be trusted — only the verifier's claims count")
}

// Pre-verify RP binding: an attestation minted for another RP is dropped by the
// local pre-check BEFORE the verifier is consulted.
func TestOpenAndVerify_RPMismatchPreVerify(t *testing.T) {
	sk, keys := newKey(t, "rp-1")
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	obs := &recordingObserver{}
	creds := []tmproto.SealedCredential{sealTo(t, "kid-1", sk.PublicKey(), attestation("rp-EVIL", tmproto.AttestationClaimUniqueHuman))}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), obs)
	assert.Empty(t, got)
	assert.Equal(t, 0, v.calls, "verifier MUST NOT be called for a proof minted for another RP")
	assert.Equal(t, []string{targeting.PreCheckRPMismatch}, obs.drops)
}

// Post-verify RP binding (defense-in-depth): the verifier returns an identity
// bound to a different RP than the recipient key commits to. Dropped with the
// distinct post-verify reason so a misbehaving verifier is observable.
func TestOpenAndVerify_RPMismatchPostVerify(t *testing.T) {
	sk, keys := newKey(t, "rp-1")
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}, rpOverride: "rp-OTHER"}
	obs := &recordingObserver{}
	creds := []tmproto.SealedCredential{sealTo(t, "kid-1", sk.PublicKey(), attestation("rp-1", tmproto.AttestationClaimUniqueHuman))}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), obs)
	assert.Empty(t, got)
	assert.Equal(t, []string{targeting.DropRPMismatchPostVerify}, obs.drops)
}

// A verifier error yields no identity and is reported via VerifierFailed (not a
// Dropped reason) so a down verifier is distinguishable from organic absence.
func TestOpenAndVerify_VerifierError(t *testing.T) {
	sk, keys := newKey(t, "rp-1")
	v := &stubVerifier{err: errors.New("verifier down")}
	obs := &recordingObserver{}
	creds := []tmproto.SealedCredential{sealTo(t, "kid-1", sk.PublicKey(), attestation("rp-1", tmproto.AttestationClaimUniqueHuman))}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), obs)
	assert.Empty(t, got)
	assert.Empty(t, obs.drops)
	assert.Equal(t, 1, obs.verifierFails)
}

// Missing signal_binding is dropped pre-verify (replay defense), verifier never
// called.
func TestOpenAndVerify_MissingSignalBinding(t *testing.T) {
	sk, keys := newKey(t, "rp-1")
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	obs := &recordingObserver{}
	att := attestation("rp-1", tmproto.AttestationClaimUniqueHuman)
	att.SignalBinding = ""
	creds := []tmproto.SealedCredential{sealTo(t, "kid-1", sk.PublicKey(), att)}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), obs)
	assert.Empty(t, got)
	assert.Equal(t, 0, v.calls)
	assert.Equal(t, []string{targeting.PreCheckNoBinding}, obs.drops)
}

// Multi-audience: only the credential addressed to a held key is opened; the
// other audience's entry is silently ignored (no drop metric).
func TestOpenAndVerify_MultiAudienceOnlyHeldKey(t *testing.T) {
	sk, keys := newKey(t, "rp-1") // hold kid-1
	other, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	obs := &recordingObserver{}
	creds := []tmproto.SealedCredential{
		sealTo(t, "kid-2", other.PublicKey(), attestation("rp-1", tmproto.AttestationClaimUniqueHuman)),
		sealTo(t, "kid-1", sk.PublicKey(), attestation("rp-1", tmproto.AttestationClaimUniqueHuman)),
	}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), obs)
	assert.Len(t, got, 1)
	assert.Equal(t, 1, v.calls, "only the held-key credential reaches the verifier")
	assert.Empty(t, obs.drops, "an unheld audience is ignored, not dropped")
}

// DoS: the count is bounded before any crypto, so the verifier is called at
// most MaxSealedCredentials times regardless of how many entries arrived.
func TestOpenAndVerify_BoundsCountBeforeCrypto(t *testing.T) {
	sk, keys := newKey(t, "rp-1")
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	creds := make([]tmproto.SealedCredential, 0, targeting.MaxSealedCredentials+5)
	for i := 0; i < targeting.MaxSealedCredentials+5; i++ {
		creds = append(creds, sealTo(t, "kid-1", sk.PublicKey(), attestation("rp-1", tmproto.AttestationClaimUniqueHuman)))
	}

	targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), nil)
	assert.LessOrEqual(t, v.calls, targeting.MaxSealedCredentials)
}

// DoS: an oversized payload is dropped at the size check, before any HPKE open.
func TestOpenAndVerify_DropsOversizedBeforeOpen(t *testing.T) {
	_, keys := newKey(t, "rp-1")
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	obs := &recordingObserver{}
	creds := []tmproto.SealedCredential{{AudienceKID: "kid-1", Payload: strings.Repeat("A", targeting.MaxSealedPayloadBytes+1)}}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), obs)
	assert.Empty(t, got)
	assert.Equal(t, 0, v.calls)
	assert.Equal(t, []string{targeting.DropOpenFailed}, obs.drops)
}

// A sealed entry missing audience_kid or payload is a malformed envelope.
func TestOpenAndVerify_MalformedEnvelope(t *testing.T) {
	_, keys := newKey(t, "rp-1")
	v := &stubVerifier{nullifier: "N"}
	obs := &recordingObserver{}
	creds := []tmproto.SealedCredential{{AudienceKID: "", Payload: "x"}}

	got := targeting.OpenAndVerify(t.Context(), creds, keys, v, baseCtx(), obs)
	assert.Empty(t, got)
	assert.Equal(t, []string{targeting.DropMalformedSeal}, obs.drops)
}

// identityWithAtt wraps an attestation on a world_id_nullifier identity entry,
// the in-band (Mechanism A) carrier VerifyAttestations consumes.
func identityWithAtt(att tmproto.Attestation) tmproto.IdentityToken {
	return tmproto.IdentityToken{UIDType: tmproto.UIDTypeWorldIDNullifier, UserToken: "0xnf", Attestation: &att}
}

// attCtx is baseCtx with the receiver's single expected relying party set — the
// RP the in-band attestation must be bound to.
func attCtx(rp string) targeting.VerifyContext {
	v := baseCtx()
	v.ExpectedRelyingPartyID = rp
	return v
}

// Fail-closed by omission: no verifier, no expected RP, or no identities ⇒ nil
// and the verifier is never consulted.
func TestVerifyAttestations_FailClosedByOmission(t *testing.T) {
	ids := []tmproto.IdentityToken{identityWithAtt(attestation("rp-1", tmproto.AttestationClaimUniqueHuman))}
	assert.Nil(t, targeting.VerifyAttestations(t.Context(), ids, nil, attCtx("rp-1"), nil))
	assert.Nil(t, targeting.VerifyAttestations(t.Context(), ids, &stubVerifier{}, baseCtx(), nil), "no expected RP ⇒ absent")
	assert.Nil(t, targeting.VerifyAttestations(t.Context(), nil, &stubVerifier{}, attCtx("rp-1"), nil))
}

// Verify-before-trust: claims come from the verifier, never the asserted
// attestation — the asserted age_over_21 does not leak through.
func TestVerifyAttestations_TrustsVerifierNotAttestation(t *testing.T) {
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	ids := []tmproto.IdentityToken{identityWithAtt(attestation("rp-1", tmproto.AttestationClaimAgeOver21))}

	got := targeting.VerifyAttestations(t.Context(), ids, v, attCtx("rp-1"), nil)
	require.Len(t, got, 1)
	assert.Equal(t, "rp-1", got[0].RelyingPartyID)
	assert.True(t, got[0].ClaimsSatisfy(tmproto.AttestationClaimUniqueHuman))
	assert.False(t, got[0].ClaimsSatisfy(tmproto.AttestationClaimAgeOver21),
		"the asserted age_over_21 must NOT be trusted — only the verifier's claims count")
}

// An attestation minted for another RP is dropped by the local pre-check before
// the verifier is consulted.
func TestVerifyAttestations_RPMismatchPreVerify(t *testing.T) {
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	obs := &recordingObserver{}
	ids := []tmproto.IdentityToken{identityWithAtt(attestation("rp-EVIL", tmproto.AttestationClaimUniqueHuman))}

	got := targeting.VerifyAttestations(t.Context(), ids, v, attCtx("rp-1"), obs)
	assert.Empty(t, got)
	assert.Equal(t, 0, v.calls, "verifier MUST NOT be called for a proof minted for another RP")
	assert.Equal(t, []string{targeting.PreCheckRPMismatch}, obs.drops)
}

// Identity entries without an attestation are ordinary tokens, skipped without
// reaching the verifier.
func TestVerifyAttestations_SkipsEntriesWithoutAttestation(t *testing.T) {
	v := &stubVerifier{nullifier: "N", claims: []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman}}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: "maid-1"},
		identityWithAtt(attestation("rp-1", tmproto.AttestationClaimUniqueHuman)),
	}

	got := targeting.VerifyAttestations(t.Context(), ids, v, attCtx("rp-1"), nil)
	assert.Len(t, got, 1)
	assert.Equal(t, 1, v.calls, "only the entry carrying an attestation reaches the verifier")
}

// A verifier error yields no identity and is reported via VerifierFailed, not a
// Dropped reason, so a down verifier is distinguishable from organic absence.
func TestVerifyAttestations_VerifierError(t *testing.T) {
	v := &stubVerifier{err: errors.New("verifier down")}
	obs := &recordingObserver{}
	ids := []tmproto.IdentityToken{identityWithAtt(attestation("rp-1", tmproto.AttestationClaimUniqueHuman))}

	got := targeting.VerifyAttestations(t.Context(), ids, v, attCtx("rp-1"), obs)
	assert.Empty(t, got)
	assert.Empty(t, obs.drops)
	assert.Equal(t, 1, obs.verifierFails)
}

func TestRejectByVerifiedIdentity(t *testing.T) {
	human := []targeting.VerifiedIdentity{{Nullifier: "N", RelyingPartyID: "rp-1", Claims: targeting.NormalizeClaims([]tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman, tmproto.AttestationClaimAgeOver18})}}

	t.Run("no requirement is never rejected", func(t *testing.T) {
		rejected := targeting.RejectByVerifiedIdentity(map[string]targeting.VerifiedIdentityRequirement{"open": {}}, nil)
		_, ok := rejected["open"]
		assert.False(t, ok)
	})

	t.Run("required but absent is rejected (fail-closed)", func(t *testing.T) {
		rejected := targeting.RejectByVerifiedIdentity(map[string]targeting.VerifiedIdentityRequirement{
			"human": {RequiresVerifiedHuman: true},
		}, nil)
		_, ok := rejected["human"]
		assert.True(t, ok)
	})

	t.Run("present verified human clears the human gate", func(t *testing.T) {
		rejected := targeting.RejectByVerifiedIdentity(map[string]targeting.VerifiedIdentityRequirement{
			"human": {RequiresVerifiedHuman: true},
		}, human)
		_, ok := rejected["human"]
		assert.False(t, ok)
	})

	t.Run("age threshold above the verified claim is rejected", func(t *testing.T) {
		rejected := targeting.RejectByVerifiedIdentity(map[string]targeting.VerifiedIdentityRequirement{
			"age21": {RequiresAge: true, RequiredAge: tmproto.AttestationClaimAgeOver21},
			"age18": {RequiresAge: true, RequiredAge: tmproto.AttestationClaimAgeOver18},
		}, human)
		_, rejected21 := rejected["age21"]
		_, rejected18 := rejected["age18"]
		assert.True(t, rejected21, "age_over_18 does not satisfy a 21 gate")
		assert.False(t, rejected18, "age_over_18 satisfies an 18 gate")
	})

	// Fail-closed: an age requirement whose threshold resolved to the empty
	// claim (RequiresAge true, RequiredAge "") is rejected — no verified
	// identity can satisfy an empty claim. RequiresAge must not be inferred from
	// a non-empty RequiredAge, or this requirement silently evaporates.
	t.Run("age required with empty threshold is rejected (fail-closed)", func(t *testing.T) {
		reqs := map[string]targeting.VerifiedIdentityRequirement{"age": {RequiresAge: true}}
		rejectedEmpty := targeting.RejectByVerifiedIdentity(reqs, nil)
		_, absentRejected := rejectedEmpty["age"]
		assert.True(t, absentRejected, "age required + no verified identity ⇒ rejected")

		rejectedPresent := targeting.RejectByVerifiedIdentity(reqs, human)
		_, presentRejected := rejectedPresent["age"]
		assert.True(t, presentRejected, "empty age claim is satisfiable by no identity ⇒ rejected")
	})
}
