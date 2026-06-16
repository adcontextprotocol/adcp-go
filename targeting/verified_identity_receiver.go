package targeting

import (
	"context"
	"crypto/ecdh"
	"encoding/json"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// verified_identity_receiver.go is the shared verify-before-trust receiver: the
// loop that turns inbound sealed_credentials into trusted VerifiedIdentity
// values, and the fail-closed gate that decides which packages a verified
// identity makes eligible. Both the in-process identity-agent and any external
// network-as-RP operator run the SAME code here, so the security-critical
// ordering (bound before crypto, local RP-binding before the verifier,
// defense-in-depth RP recheck after) has exactly one home.

const (
	// MaxSealedCredentials caps sealed_credentials entries processed per
	// request (schema maxItems). Enforced before any crypto so a flood of
	// garbage entries cannot force N HPKE opens + verifier round-trips.
	MaxSealedCredentials = 8
	// MaxSealedPayloadBytes caps one sealed payload's wire length (schema
	// maxLength 8192) before opening.
	MaxSealedPayloadBytes = 8192
)

// Receiver drop reasons emitted by OpenAndVerify that are not already covered
// by the PreCheck* constants. Each is a bounded metric label.
const (
	// DropMalformedSeal is a sealed_credentials entry missing audience_kid or
	// payload — the envelope is malformed before any attestation is parsed.
	DropMalformedSeal = "malformed_sealed"
	// DropOpenFailed is an oversized payload, a failed HPKE open, or plaintext
	// that does not decode to an attestation.
	DropOpenFailed = "open_failed"
	// DropRPMismatchPostVerify is the verifier returning an identity bound to a
	// different relying party than the recipient key commits to. Distinct from
	// the pre-verify PreCheckRPMismatch: this branch is defense-in-depth and
	// should never fire — when it does, the verifier itself is misbehaving, not
	// a forwarded proof.
	DropRPMismatchPostVerify = "rp_mismatch_postverify"
)

// RecipientKey is the HPKE recipient identity for one audience_kid: the X25519
// private key that opens sealed_credentials addressed to that kid, plus the
// relying party THIS receiver acts as for that audience. RelyingPartyID is the
// receiver-binding anchor — a sealed attestation whose relying_party_id does
// not equal it is a forwarded/replayed proof and is rejected (LocalPreCheck).
// The audience_kid that selected this key is how the receiver knows which RP it
// is, so the binding is enforced locally and fail-closed rather than deferred
// to the verifier.
type RecipientKey struct {
	PrivateKey     *ecdh.PrivateKey
	RelyingPartyID string
}

// CredentialObserver receives per-credential observations from OpenAndVerify so
// a receiver can record metrics without OpenAndVerify owning a metric
// vocabulary. A nil observer is replaced with a no-op.
type CredentialObserver interface {
	// Dropped fires once per credential treated as absent, with a PreCheck* or
	// Drop* reason label.
	Dropped(ctx context.Context, reason string)
	// VerifierFailed fires when the verifier itself returned an error — kept
	// distinct from Dropped so a down/misconfigured verifier is observable, not
	// mistaken for organic "no humans verified".
	VerifierFailed(ctx context.Context)
}

type noopCredentialObserver struct{}

func (noopCredentialObserver) Dropped(context.Context, string) {}
func (noopCredentialObserver) VerifierFailed(context.Context)  {}

// OpenAndVerify is the verify-before-trust receiver loop. For each inbound
// sealed credential addressed to a held recipient key it HPKE-opens the
// payload, enforces the local fail-closed pre-checks, calls the pluggable
// verifier, and returns the verified identities with claims normalized to the
// closed claim set.
//
// Fail-closed by construction. With no verifier or no recipient keys it returns
// nil immediately (no opening, no allocation). Opening a credential proves only
// that it was sealed to our key — never that the claims are true. Claims become
// eligible inputs only after the verifier validates the proof AND the local
// pre-checks pass (signal_binding present, not expired, relying_party_id == the
// RP we act as). The count is bounded before any crypto, and an undecryptable
// blob costs at most local work because the verifier is consulted only after a
// successful open.
//
// vctx supplies RequestID, Country, and Now; ExpectedRelyingPartyID is filled
// per-credential from the matched recipient key.
func OpenAndVerify(
	ctx context.Context,
	creds []tmproto.SealedCredential,
	keys map[string]RecipientKey,
	verifier AttestationVerifier,
	vctx VerifyContext,
	obs CredentialObserver,
) []VerifiedIdentity {
	if verifier == nil || len(keys) == 0 || len(creds) == 0 {
		return nil
	}
	if obs == nil {
		obs = noopCredentialObserver{}
	}
	// Bound the count before any crypto (DoS): never open more than the schema
	// maximum, regardless of how many entries arrived.
	if len(creds) > MaxSealedCredentials {
		creds = creds[:MaxSealedCredentials]
	}

	var verified []VerifiedIdentity
	for _, c := range creds {
		if c.AudienceKID == "" || c.Payload == "" {
			obs.Dropped(ctx, DropMalformedSeal)
			continue
		}
		rk, ok := keys[c.AudienceKID]
		if !ok {
			// Not addressed to an audience we hold a key for. Correct
			// multi-audience behavior — silently ignore, no drop metric.
			continue
		}
		if len(c.Payload) > MaxSealedPayloadBytes {
			obs.Dropped(ctx, DropOpenFailed)
			continue
		}
		// HPKE-open (cheap, local) with the sealed-credential size budget. Only
		// AFTER it succeeds do we consider the network verifier, so
		// undecryptable blobs cost at most local work.
		pt, _, err := tmproto.OpenSealedCredential(rk.PrivateKey, nil, c.Payload)
		if err != nil {
			obs.Dropped(ctx, DropOpenFailed)
			continue
		}
		var att tmproto.Attestation
		if err := json.Unmarshal(pt, &att); err != nil {
			obs.Dropped(ctx, DropOpenFailed)
			continue
		}
		credVctx := vctx
		credVctx.ExpectedRelyingPartyID = rk.RelyingPartyID
		if vi, ok := verifyOne(ctx, &att, verifier, credVctx, obs); ok {
			verified = append(verified, vi)
		}
	}
	return verified
}

// verifyOne runs the verify-before-trust checks for a single attestation whose
// expected relying party is already set on vctx: the local, fail-closed
// pre-checks (signal_binding present, not expired, relying_party_id == the RP
// we act as) BEFORE the possibly-network verifier, then the verifier, then the
// defense-in-depth post-verify RP recheck. It returns the VerifiedIdentity with
// claims normalized to the closed set so the age gate only ever sees canonical
// values. ok is false — and the matching observer signal has already fired —
// when the attestation must be treated as absent.
func verifyOne(ctx context.Context, att *tmproto.Attestation, verifier AttestationVerifier, vctx VerifyContext, obs CredentialObserver) (VerifiedIdentity, bool) {
	if reason := LocalPreCheck(att, vctx); reason != PreCheckOK {
		obs.Dropped(ctx, reason)
		return VerifiedIdentity{}, false
	}
	vi, err := verifier.Verify(ctx, att, vctx)
	if err != nil {
		obs.VerifierFailed(ctx)
		return VerifiedIdentity{}, false
	}
	if vi.RelyingPartyID != vctx.ExpectedRelyingPartyID {
		obs.Dropped(ctx, DropRPMismatchPostVerify)
		return VerifiedIdentity{}, false
	}
	vi.Claims = normalizeClaimSet(vi.Claims)
	return vi, true
}

// VerifyAttestations is the in-band sibling of OpenAndVerify: it runs the
// verify-before-trust checks on the attestations carried directly on the
// request's identity entries (req.Identities[].Attestation), with no HPKE seal
// to open, and returns the verified identities. The same rule holds — claims
// are trusted only from the verifier, never the asserted attestation — and the
// expected relying party is vctx.ExpectedRelyingPartyID, the single RP this
// receiver acts as; an attestation bound to any other RP is dropped by
// LocalPreCheck.
//
// Fail-closed by construction: with no verifier or no expected relying party it
// returns nil without consulting the verifier. An identity entry without an
// attestation is an ordinary token, not a verified identity, and is skipped.
func VerifyAttestations(ctx context.Context, identities []tmproto.IdentityToken, verifier AttestationVerifier, vctx VerifyContext, obs CredentialObserver) []VerifiedIdentity {
	if verifier == nil || vctx.ExpectedRelyingPartyID == "" || len(identities) == 0 {
		return nil
	}
	if obs == nil {
		obs = noopCredentialObserver{}
	}
	var verified []VerifiedIdentity
	for i := range identities {
		att := identities[i].Attestation
		if att == nil {
			continue
		}
		if vi, ok := verifyOne(ctx, att, verifier, vctx, obs); ok {
			verified = append(verified, vi)
		}
	}
	return verified
}

// VerifiedIdentityRequirement is one package's verified-identity requirement,
// resolved by the caller from its own package config and age policy.
type VerifiedIdentityRequirement struct {
	// RequiresVerifiedHuman gates the package on at least one present verified
	// identity (a verified unique human).
	RequiresVerifiedHuman bool
	// RequiresAge gates the package on a verified age threshold. When set, a
	// verified identity must satisfy RequiredAge. RequiredAge may be empty even
	// when RequiresAge is set (the policy applies but resolved no closed-set
	// claim); no verified identity can satisfy an empty claim, so the package
	// is then rejected — fail-closed. Carried separately from RequiredAge so
	// "age required, threshold empty" is not collapsed into "no age
	// requirement".
	RequiresAge bool
	// RequiredAge is the age claim a verified identity must satisfy when
	// RequiresAge is set.
	RequiredAge tmproto.AttestationClaim
}

// RejectByVerifiedIdentity returns the package IDs rejected by the
// verified-identity gate: a package that requires a verified human, or an age
// threshold, that no verified identity satisfies. Fail-closed —
// required-but-absent ⇒ rejected. A package with no requirement is never
// rejected here.
func RejectByVerifiedIdentity(reqs map[string]VerifiedIdentityRequirement, verified []VerifiedIdentity) map[string]struct{} {
	rejected := make(map[string]struct{})
	for id, r := range reqs {
		if !r.RequiresVerifiedHuman && !r.RequiresAge {
			continue // package has no verified-identity requirement
		}
		if len(verified) == 0 {
			rejected[id] = struct{}{} // required but absent — fail closed
			continue
		}
		if r.RequiresAge && !anyVerifiedSatisfies(verified, r.RequiredAge) {
			rejected[id] = struct{}{}
		}
	}
	return rejected
}

func anyVerifiedSatisfies(verified []VerifiedIdentity, required tmproto.AttestationClaim) bool {
	for _, vi := range verified {
		if vi.ClaimsSatisfy(required) {
			return true
		}
	}
	return false
}

// normalizeClaimSet drops any claim not in the closed attestation-claim set,
// applying the same closed-set policy as NormalizeClaims to a claim set the
// verifier already returned as a map.
func normalizeClaimSet(in map[tmproto.AttestationClaim]struct{}) map[tmproto.AttestationClaim]struct{} {
	claims := make([]tmproto.AttestationClaim, 0, len(in))
	for c := range in {
		claims = append(claims, c)
	}
	return NormalizeClaims(claims)
}
