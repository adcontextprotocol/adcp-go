package identityagent

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// RecipientKey is the HPKE recipient identity for one audience_kid: the X25519
// private key that opens sealed_credentials addressed to that kid, plus the
// relying party THIS deployment acts as for that audience. RelyingPartyID is
// the receiver-binding anchor — a sealed attestation whose relying_party_id
// does not equal it is a forwarded/replayed proof and is rejected
// (targeting.LocalPreCheck). The audience_kid that selected this key is how the
// deployment knows which RP it is, so the binding is enforced locally and
// fail-closed rather than deferred to the verifier.
type RecipientKey struct {
	PrivateKey     *ecdh.PrivateKey
	RelyingPartyID string
}

const (
	// maxSealedCredentials caps sealed_credentials entries processed per
	// request (schema maxItems). Enforced before any crypto so a flood of
	// garbage entries cannot force N HPKE opens + verifier round-trips.
	maxSealedCredentials = 8
	// maxSealedPayloadBytes caps one sealed payload's wire length (schema
	// maxLength 8192) before opening.
	maxSealedPayloadBytes = 8192
)

// runVerifiedIdentityStage opens and verifies the request's sealed_credentials
// (the network-as-RP / Mechanism B carrier) and returns the verified
// identities.
//
// Fail-closed: with no verifier or no recipient keys configured it returns nil
// immediately (no opening, no allocation), so existing eligibility behavior is
// byte-for-byte unchanged. Opening a credential proves only that it was sealed
// to our key — never that the claims are true. Claims are trusted only after
// the pluggable verifier validates the proof AND the local pre-checks pass
// (signal_binding present, not expired, relying_party_id == the RP we act as).
func (s *Service) runVerifiedIdentityStage(ctx context.Context, req *tmproto.IdentityMatchRequest) []targeting.VerifiedIdentity {
	if s.verifier == nil || len(s.recipientKeys) == 0 || len(req.SealedCredentials) == 0 {
		return nil
	}
	start := time.Now()
	now := start
	// The verifier is called on the parent ctx (the handler's request budget
	// bounds a well-behaved, ctx-respecting verifier). A dedicated per-stage
	// sub-timeout + OutcomeTimeout — mirroring runFcapStage/runAudienceStage —
	// lands with the concrete network verifier, which is when a stalled
	// verifier becomes a real risk; the nil/mock verifiers here are instant.

	// Bound the count before any crypto (DoS): never open more than the
	// schema maximum, regardless of how many entries arrived.
	creds := req.SealedCredentials
	if len(creds) > maxSealedCredentials {
		creds = creds[:maxSealedCredentials]
	}

	var verified []targeting.VerifiedIdentity
	verifierErrored := false
	for _, c := range creds {
		if c.AudienceKID == "" || c.Payload == "" {
			s.recorder.VerifiedIdentityDrop(ctx, VIDropMalformedSeal)
			continue
		}
		rk, ok := s.recipientKeys[c.AudienceKID]
		if !ok {
			// Not addressed to an audience we hold a key for. Correct
			// multi-audience behavior — silently ignore, no drop metric.
			continue
		}
		if len(c.Payload) > maxSealedPayloadBytes {
			s.recorder.VerifiedIdentityDrop(ctx, VIDropOpenFailed)
			continue
		}
		// HPKE-open (cheap, local) with the sealed-credential size budget.
		// Only AFTER it succeeds do we consider the network verifier, so
		// undecryptable blobs cost at most local work.
		pt, _, err := tmproto.OpenSealedCredential(rk.PrivateKey, nil, c.Payload)
		if err != nil {
			s.recorder.VerifiedIdentityDrop(ctx, VIDropOpenFailed)
			continue
		}
		var att tmproto.Attestation
		if err := json.Unmarshal(pt, &att); err != nil {
			s.recorder.VerifiedIdentityDrop(ctx, VIDropOpenFailed)
			continue
		}
		vctx := targeting.VerifyContext{
			RequestID:              req.RequestID,
			Country:                req.Country,
			ExpectedRelyingPartyID: rk.RelyingPartyID,
			Now:                    now,
		}
		// Local, fail-closed invariants BEFORE the (possibly network)
		// verifier: empty signal_binding, expired, or RP mismatch ⇒ absent.
		if reason := targeting.LocalPreCheck(&att, vctx); reason != targeting.PreCheckOK {
			s.recorder.VerifiedIdentityDrop(ctx, reason)
			continue
		}
		vi, err := s.verifier.Verify(ctx, &att, vctx)
		if err != nil {
			verifierErrored = true
			s.recorder.VerifiedIdentityDrop(ctx, VIDropVerifierError)
			continue
		}
		// Defense-in-depth: the verifier must return the RP we bound to.
		if vi.RelyingPartyID != rk.RelyingPartyID {
			s.recorder.VerifiedIdentityDrop(ctx, targeting.PreCheckRPMismatch)
			continue
		}
		// Normalize to the closed claim set so the age gate only ever sees
		// canonical values, regardless of verifier behavior.
		vi.Claims = normalizeClaimSet(vi.Claims)
		verified = append(verified, vi)
	}

	s.recorder.StageDuration(ctx, StageVerifiedIdentity, time.Since(start))
	// A verifier error is recorded as an error outcome (not pass) so a
	// down/misconfigured verifier is observable instead of indistinguishable
	// from organic "no humans verified".
	outcome := OutcomePass
	if verifierErrored {
		outcome = OutcomeError
	}
	s.recorder.StageOutcome(ctx, StageVerifiedIdentity, outcome)
	return verified
}

// normalizeClaimSet drops any claim not in the closed attestation-claim set.
func normalizeClaimSet(in map[tmproto.AttestationClaim]struct{}) map[tmproto.AttestationClaim]struct{} {
	out := make(map[tmproto.AttestationClaim]struct{}, len(in))
	for c := range in {
		if targeting.IsClosedClaim(c) {
			out[c] = struct{}{}
		}
	}
	return out
}

// computeVerifiedIdentityGate returns the packages ineligible because of the
// verified-identity gate: a package that requires verified identity when none
// is present, or a package whose resolved age threshold no verified identity
// satisfies. Computed synchronously (outside the parallel stages) so a cancel
// race cannot drop the verdict — this is the fail-closed third gate, and
// joinResults defaults packages to eligible.
func (s *Service) computeVerifiedIdentityGate(ctx context.Context, req *tmproto.IdentityMatchRequest, resolved *targeting.ResolvedPackages, pkgIDs []string, verified []targeting.VerifiedIdentity) map[string]struct{} {
	rejected := make(map[string]struct{})
	for _, id := range pkgIDs {
		cfg := resolved.IdentityConfigs[id]
		requiresVID := cfg != nil && cfg.RequiresVerifiedIdentity

		var ageClaim tmproto.AttestationClaim
		ageRequired := false
		if s.ageResolver != nil {
			ageClaim, ageRequired = s.ageResolver.ResolveRequiredAge(ctx, id, req.Country)
		}

		if !requiresVID && !ageRequired {
			continue // package has no verified-identity requirement
		}
		if len(verified) == 0 {
			rejected[id] = struct{}{} // required but absent — fail closed
			continue
		}
		if ageRequired && !anyVerifiedSatisfies(verified, ageClaim) {
			rejected[id] = struct{}{}
		}
	}
	return rejected
}

func anyVerifiedSatisfies(verified []targeting.VerifiedIdentity, required tmproto.AttestationClaim) bool {
	for _, vi := range verified {
		if vi.ClaimsSatisfy(required) {
			return true
		}
	}
	return false
}
