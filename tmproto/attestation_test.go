package tmproto

import (
	"errors"
	"testing"
	"time"
)

func sampleAttestation() *Attestation {
	return &Attestation{
		Issuer:            map[string]any{"domain": "world.org"},
		Scheme:            "world_id_v4",
		RelyingPartyID:    "rp-publisher",
		Claims:            []AttestationClaim{AttestationClaimUniqueHuman, AttestationClaimAgeOver18},
		VerificationLevel: "orb",
		SignalBinding:     "keccak-of-nonce",
		Proof:             map[string]any{"merkle_root": "0xabc", "nullifier_hash": "0xdef"},
		ExpiresAt:         "2026-06-09T00:00:00Z",
	}
}

func sampleSealedCredentials() []SealedCredential {
	return []SealedCredential{
		{AudienceKID: "k_net_1", Payload: "k_net_1.ZmFrZQ"},
		{AudienceKID: "k_net_2", Payload: "k_net_2.b3RoZXI"},
	}
}

// An attestation on an identity is part of the signed canonical bytes: the
// request verifies with it intact and fails when any field is altered.
func TestSignerIdentityMatchAttestationCovered(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"

	req := &IdentityMatchRequest{
		RequestID:      "req-att",
		SellerAgentURL: "https://seller.example.com/agent",
		Identities: []IdentityToken{
			{UIDType: UIDTypeWorldIDNullifier, UserToken: "0xnullifier", Attestation: sampleAttestation()},
		},
	}
	sig, err := signer.SignIdentityMatch(req, endpoint, EpochAt(now))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyIdentityMatch(req, endpoint, sig, signer.KeyID, ks, now); err != nil {
		t.Fatalf("verify intact attestation: %v", err)
	}

	// Tamper a claim — verification must fail.
	tampered := *req
	tampered.Identities = []IdentityToken{*deepCopyIdentity(&req.Identities[0])}
	tampered.Identities[0].Attestation.Claims = []AttestationClaim{AttestationClaimUniqueHuman, AttestationClaimAgeOver21}
	if err := VerifyIdentityMatch(&tampered, endpoint, sig, signer.KeyID, ks, now); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid after claim tamper, got %v", err)
	}
}

// Stripping the attestation breaks verification — an attacker cannot drop a
// proof and have the signature still match.
func TestSignerIdentityMatchAttestationStripBreaksSignature(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"

	req := &IdentityMatchRequest{
		RequestID:  "req-strip",
		Identities: []IdentityToken{{UIDType: UIDTypeWorldIDNullifier, UserToken: "0xn", Attestation: sampleAttestation()}},
	}
	sig, err := signer.SignIdentityMatch(req, endpoint, EpochAt(now))
	if err != nil {
		t.Fatal(err)
	}
	stripped := *req
	stripped.Identities = []IdentityToken{{UIDType: UIDTypeWorldIDNullifier, UserToken: "0xn"}}
	if err := VerifyIdentityMatch(&stripped, endpoint, sig, signer.KeyID, ks, now); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid after stripping attestation, got %v", err)
	}
}

// An identity without an attestation serializes to exactly {uid_type,
// user_token}, so adding an attestation changes the signing input. This pins
// the backward-compatible shape: no attestation == the pre-attestation bytes.
func TestSignerIdentityMatchAttestationChangesInput(t *testing.T) {
	endpoint := "https://provider.example.com"
	without := &IdentityMatchRequest{
		RequestID:  "r",
		Identities: []IdentityToken{{UIDType: UIDTypeWorldIDNullifier, UserToken: "0xn"}},
	}
	with := &IdentityMatchRequest{
		RequestID:  "r",
		Identities: []IdentityToken{{UIDType: UIDTypeWorldIDNullifier, UserToken: "0xn", Attestation: sampleAttestation()}},
	}
	iw, err := BuildIdentityMatchSigningInput(without, endpoint, 20000)
	if err != nil {
		t.Fatal(err)
	}
	iwith, err := BuildIdentityMatchSigningInput(with, endpoint, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if string(iw) == string(iwith) {
		t.Fatal("attestation must change the signing input")
	}
}

// sealed_credentials are folded into the signed bytes: tampering the payload
// breaks verification, while reordering entries (canonicalized by audience_kid)
// does not.
func TestSignerIdentityMatchSealedCredentialsCovered(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"

	req := &IdentityMatchRequest{
		RequestID:         "req-sealed",
		Identities:        []IdentityToken{{UIDType: UIDTypeUID2, UserToken: "tok"}},
		SealedCredentials: sampleSealedCredentials(),
	}
	sig, err := signer.SignIdentityMatch(req, endpoint, EpochAt(now))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyIdentityMatch(req, endpoint, sig, signer.KeyID, ks, now); err != nil {
		t.Fatalf("verify intact sealed credentials: %v", err)
	}

	// Reordered entries verify (canonical sort is by audience_kid).
	reordered := *req
	reordered.SealedCredentials = []SealedCredential{req.SealedCredentials[1], req.SealedCredentials[0]}
	if err := VerifyIdentityMatch(&reordered, endpoint, sig, signer.KeyID, ks, now); err != nil {
		t.Fatalf("reordered sealed credentials should verify: %v", err)
	}

	// Tampered payload fails.
	tampered := *req
	tampered.SealedCredentials = []SealedCredential{{AudienceKID: "k_net_1", Payload: "k_net_1.dGFtcGVy"}, req.SealedCredentials[1]}
	if err := VerifyIdentityMatch(&tampered, endpoint, sig, signer.KeyID, ks, now); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid after sealed payload tamper, got %v", err)
	}
}

// An injected sealed credential breaks verification even though the original
// entries are untouched.
func TestSignerIdentityMatchSealedCredentialInjectionBreaksSignature(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"

	req := &IdentityMatchRequest{
		RequestID:         "r",
		Identities:        []IdentityToken{{UIDType: UIDTypeUID2, UserToken: "tok"}},
		SealedCredentials: []SealedCredential{{AudienceKID: "k1", Payload: "k1.AAAA"}},
	}
	sig, err := signer.SignIdentityMatch(req, endpoint, EpochAt(now))
	if err != nil {
		t.Fatal(err)
	}
	injected := *req
	injected.SealedCredentials = append([]SealedCredential{{AudienceKID: "k0", Payload: "k0.BBBB"}}, req.SealedCredentials...)
	if err := VerifyIdentityMatch(&injected, endpoint, sig, signer.KeyID, ks, now); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid after sealed-credential injection, got %v", err)
	}
}

// sealed_credentials_hash is null when absent, so adding sealed credentials
// changes the signing input.
func TestSignerIdentityMatchSealedCredentialsHashNullWhenAbsent(t *testing.T) {
	endpoint := "https://provider.example.com"
	base := &IdentityMatchRequest{RequestID: "r", Identities: []IdentityToken{{UIDType: UIDTypeUID2, UserToken: "tok"}}}
	withSealed := *base
	withSealed.SealedCredentials = sampleSealedCredentials()

	iabsent, err := BuildIdentityMatchSigningInput(base, endpoint, 20000)
	if err != nil {
		t.Fatal(err)
	}
	ipresent, err := BuildIdentityMatchSigningInput(&withSealed, endpoint, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if string(iabsent) == string(ipresent) {
		t.Fatal("sealed_credentials must change the signing input")
	}

	// An empty (non-nil) slice is treated as absent — same input as no field.
	emptySlice := *base
	emptySlice.SealedCredentials = []SealedCredential{}
	iempty, err := BuildIdentityMatchSigningInput(&emptySlice, endpoint, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if string(iempty) != string(iabsent) {
		t.Fatal("empty sealed_credentials slice must hash identically to absent")
	}
}

func deepCopyIdentity(id *IdentityToken) *IdentityToken {
	cp := *id
	if id.Attestation != nil {
		att := *id.Attestation
		att.Claims = append([]AttestationClaim(nil), id.Attestation.Claims...)
		cp.Attestation = &att
	}
	return &cp
}
