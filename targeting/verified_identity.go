package targeting

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// verified_identity.go is the verify-before-trust seam for TMP verified
// identity. The headline rule: opening or parsing an attestation proves
// NOTHING about its truth. Claims become eligible inputs only after an
// AttestationVerifier validates the proof AND the local, fail-closed
// pre-checks pass. An attestation that fails any check is treated as ABSENT
// (no attestation) — never as an asserted-true claim.

// VerifiedIdentity is the post-verification result for one attestation that
// passed every check: a relying-party-scoped pseudonym plus the claims the
// receiver is allowed to trust. It is the ONLY way claims enter eligibility;
// an unverified or absent attestation never produces one.
type VerifiedIdentity struct {
	// Nullifier is the relying-party-scoped, Sybil-resistant pseudonym. It is
	// the frequency-cap key (namespaced by RelyingPartyID, see CapKey) — never
	// used raw, so it cannot collide across relying parties.
	Nullifier string
	// RelyingPartyID is the RP scope this nullifier and these claims belong
	// to. The receiver confirmed it matches the RP this receiver acts as.
	RelyingPartyID string
	// Claims is the verified claim set, normalised to the closed
	// attestation-claim vocabulary.
	Claims map[tmproto.AttestationClaim]struct{}
}

// CapKey returns the frequency-cap key for this verified identity. The key is
// namespaced by relying party so the same nullifier byte-string under two
// different relying parties yields two distinct keys (no cross-RP cap
// contamination), and the "vid:" prefix segregates verified-identity keys
// from raw UserToken cap keys (no cap-poisoning by sending a guessed nullifier
// as an ordinary unverified token).
func (vi VerifiedIdentity) CapKey() string {
	return "vid:" + vi.RelyingPartyID + ":" + vi.Nullifier
}

// VerifyContext carries the request-scoped values a verifier needs to bind a
// proof to THIS request and THIS receiver (replay + provenance defense)
// without handing it the whole IdentityMatchRequest.
type VerifyContext struct {
	// RequestID is the anchor the attestation's signal_binding must commit to.
	RequestID string
	// Country is ISO-3166-1 alpha-2, for geo-scoped verifier/age logic.
	Country string
	// ExpectedRelyingPartyID is the relying party THIS receiver acts as for
	// the audience that selected the recipient key. The attestation's
	// relying_party_id must equal it (enforced SDK-side before Verify) so a
	// proof minted for another RP cannot be replayed here.
	ExpectedRelyingPartyID string
	// Now is the reference time for expiry checks.
	Now time.Time
}

// AttestationVerifier validates a wire Attestation against the AdCP
// conformance invariants for its scheme. adcp-go cannot itself verify a World
// ID zk proof, so verification is pluggable: a deployment wires a concrete
// verifier (e.g. an HTTP call to a World ID verifier service, an mDL
// validator). Verify returns the relying-party-scoped nullifier and the
// verified claim set ONLY when every scheme invariant holds.
//
//   - Verify MUST validate scheme + issuer + proof per the scheme rules and
//     confirm the proof was minted for vctx.ExpectedRelyingPartyID (the SDK
//     also enforces relying_party_id == ExpectedRelyingPartyID before calling
//     Verify, but the verifier owns the cryptographic binding + brand.json
//     ownership lookup).
//   - Verify MUST confirm the attestation's signal_binding commits to
//     vctx.RequestID (the SDK guarantees signal_binding is non-empty but
//     cannot check the scheme-specific hash).
//   - It SHOULD dedupe nullifiers within the freshness window
//     (nullifier-reuse tracking; not yet enforced SDK-side).
//   - Any failure returns a non-nil error; callers treat that as "no
//     attestation".
//
// There is intentionally no default verifier: a Service with a nil verifier
// treats every attestation as absent (fail-closed), so trust-without-verify is
// unreachable by configuration omission.
type AttestationVerifier interface {
	Verify(ctx context.Context, att *tmproto.Attestation, vctx VerifyContext) (VerifiedIdentity, error)
}

// AgeResolver resolves whether a package requires an age threshold for a given
// geo, and which closed-set claim satisfies it. ok=false means no age
// requirement applies. The production implementation resolves
// (age policy, geo) → threshold via the AdCP Policy Registry; it is pluggable
// so the registry integration lands behind this interface without changing
// the pipeline.
type AgeResolver interface {
	ResolveRequiredAge(ctx context.Context, pkgID, country string) (claim tmproto.AttestationClaim, ok bool)
}

// Pre-check drop reasons. The local pre-check runs BEFORE the (possibly
// network) verifier and before any expensive crypto on the verifier's side;
// each reason is a bounded metric label.
const (
	PreCheckOK         = ""            // passed local checks; safe to call the verifier
	PreCheckMalformed  = "malformed"   // nil attestation / unparseable
	PreCheckNoBinding  = "no_binding"  // signal_binding empty — fail closed (replay defense)
	PreCheckExpired    = "expired"     // expires_at present and in the past
	PreCheckRPMismatch = "rp_mismatch" // relying_party_id != the RP this receiver acts as
)

// LocalPreCheck enforces the invariants the SDK can check locally and
// fail-closed, before handing the attestation to the pluggable verifier.
// Returns PreCheckOK ("") when the attestation may proceed to Verify, or a
// drop reason otherwise. A dropped attestation MUST be treated as absent.
//
// These checks are deliberately NOT delegated to the verifier:
//   - signal_binding non-empty: the field is optional on the wire, so an
//     attacker could omit it; requiring it here closes the omit-and-replay
//     path regardless of verifier quality.
//   - expires_at: a present horizon must be in the future.
//   - relying_party_id receiver-binding: the audience_kid that selected our
//     recipient key tells us which RP we are; a proof for another RP is a
//     forwarded/replayed proof and is rejected here, cheaply and locally.
func LocalPreCheck(att *tmproto.Attestation, vctx VerifyContext) string {
	if att == nil {
		return PreCheckMalformed
	}
	if att.SignalBinding == "" {
		return PreCheckNoBinding
	}
	// ExpiresAt is the wire string (RFC 3339) so it round-trips byte-for-byte
	// through the request signature. A present horizon must parse and be in
	// the future; an unparseable value fails closed (treated as expired).
	if att.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, att.ExpiresAt)
		if err != nil || !exp.After(vctx.Now) {
			return PreCheckExpired
		}
	}
	if vctx.ExpectedRelyingPartyID == "" || att.RelyingPartyID != vctx.ExpectedRelyingPartyID {
		return PreCheckRPMismatch
	}
	return PreCheckOK
}

// NormalizeClaims projects a claim slice onto the closed attestation-claim
// set, dropping any value not in the vocabulary. The verified-identity
// pipeline only ever stores normalised claims, so downstream age logic
// (parseAgeOver) only ever sees canonical values.
func NormalizeClaims(claims []tmproto.AttestationClaim) map[tmproto.AttestationClaim]struct{} {
	out := make(map[tmproto.AttestationClaim]struct{}, len(claims))
	for _, c := range claims {
		if IsClosedClaim(c) {
			out[c] = struct{}{}
		}
	}
	return out
}

// IsClosedClaim reports whether c is one of the closed attestation-claim
// values. The wire format tolerates unrecognized (additive) claim values; the
// buyer-side age gate only acts on the closed set, so non-canonical values are
// dropped here before they can reach eligibility logic.
func IsClosedClaim(c tmproto.AttestationClaim) bool {
	switch c {
	case tmproto.AttestationClaimUniqueHuman,
		tmproto.AttestationClaimAgeOver13,
		tmproto.AttestationClaimAgeOver16,
		tmproto.AttestationClaimAgeOver18,
		tmproto.AttestationClaimAgeOver21:
		return true
	default:
		return false
	}
}

// ClaimsSatisfy reports whether the verified claim set satisfies a required
// claim. For an age threshold, a higher age claim satisfies a lower one
// (age_over_21 satisfies a required age_over_18). For unique_human, the set
// must contain unique_human.
func (vi VerifiedIdentity) ClaimsSatisfy(required tmproto.AttestationClaim) bool {
	if _, ok := vi.Claims[required]; ok {
		return true
	}
	want, isAge := parseAgeOver(required)
	if !isAge {
		return false
	}
	for c := range vi.Claims {
		if got, ok := parseAgeOver(c); ok && got >= want {
			return true
		}
	}
	return false
}

// parseAgeOver extracts N from an "age_over_<N>" claim. Returns ok=false for
// non-age claims (e.g. unique_human). Operates on the closed set, so the input
// is always canonical.
func parseAgeOver(c tmproto.AttestationClaim) (int, bool) {
	const prefix = "age_over_"
	s := string(c)
	if !strings.HasPrefix(s, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(s[len(prefix):])
	if err != nil {
		return 0, false
	}
	return n, true
}
