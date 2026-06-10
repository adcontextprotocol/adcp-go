package identityagent

import (
	"context"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// RecipientKey is the HPKE recipient identity for one audience_kid — the X25519
// private key plus the relying party this deployment acts as for that audience.
// See targeting.RecipientKey for the receiver-binding semantics.
type RecipientKey = targeting.RecipientKey

// stageObserver adapts the verified-identity stage's Recorder to the
// targeting.CredentialObserver the receiver loop emits to. A verifier error is
// recorded both as a drop (so its rate is visible alongside the other drop
// reasons) and as an error outcome via verifierErrored, so a
// down/misconfigured verifier is observable instead of indistinguishable from
// organic "no humans verified".
type stageObserver struct {
	recorder        Recorder
	verifierErrored bool
}

func (o *stageObserver) Dropped(ctx context.Context, reason string) {
	o.recorder.VerifiedIdentityDrop(ctx, reason)
}

func (o *stageObserver) VerifierFailed(ctx context.Context) {
	o.verifierErrored = true
	o.recorder.VerifiedIdentityDrop(ctx, VIDropVerifierError)
}

// runVerifiedIdentityStage opens and verifies the request's sealed_credentials
// (the network-as-RP / Mechanism B carrier) and returns the verified
// identities.
//
// Fail-closed: with no verifier or no recipient keys configured it returns nil
// immediately, so existing eligibility behavior is byte-for-byte unchanged.
// Opening a credential proves only that it was sealed to our key — never that
// the claims are true. The verify-before-trust loop itself lives in
// targeting.OpenAndVerify; this stage wires the request, the metric observer,
// and the stage-level duration/outcome metrics around it.
func (s *Service) runVerifiedIdentityStage(ctx context.Context, req *tmproto.IdentityMatchRequest) []targeting.VerifiedIdentity {
	if s.verifier == nil || len(s.recipientKeys) == 0 || len(req.SealedCredentials) == 0 {
		return nil
	}
	start := time.Now()
	// The verifier is called on the parent ctx (the handler's request budget
	// bounds a well-behaved, ctx-respecting verifier). A dedicated per-stage
	// sub-timeout + OutcomeTimeout — mirroring runFcapStage/runAudienceStage —
	// lands with the concrete network verifier, which is when a stalled
	// verifier becomes a real risk; the nil/mock verifiers here are instant.
	obs := &stageObserver{recorder: s.recorder}
	vctx := targeting.VerifyContext{
		RequestID: req.RequestID,
		Country:   req.Country,
		Now:       start,
	}
	verified := targeting.OpenAndVerify(ctx, req.SealedCredentials, s.recipientKeys, s.verifier, vctx, obs)

	s.recorder.StageDuration(ctx, StageVerifiedIdentity, time.Since(start))
	outcome := OutcomePass
	if obs.verifierErrored {
		outcome = OutcomeError
	}
	s.recorder.StageOutcome(ctx, StageVerifiedIdentity, outcome)
	return verified
}

// computeVerifiedIdentityGate returns the packages ineligible because of the
// verified-identity gate: a package that requires verified identity when none
// is present, or a package whose resolved age threshold no verified identity
// satisfies. Computed synchronously (outside the parallel stages) so a cancel
// race cannot drop the verdict — this is the fail-closed third gate, and
// joinResults defaults packages to eligible.
func (s *Service) computeVerifiedIdentityGate(ctx context.Context, req *tmproto.IdentityMatchRequest, resolved *targeting.ResolvedPackages, pkgIDs []string, verified []targeting.VerifiedIdentity) map[string]struct{} {
	reqs := make(map[string]targeting.VerifiedIdentityRequirement, len(pkgIDs))
	for _, id := range pkgIDs {
		cfg := resolved.IdentityConfigs[id]
		pkgReq := targeting.VerifiedIdentityRequirement{
			RequiresVerifiedHuman: cfg != nil && cfg.RequiresVerifiedIdentity,
		}
		if s.ageResolver != nil {
			if claim, ok := s.ageResolver.ResolveRequiredAge(ctx, id, req.Country); ok {
				pkgReq.RequiresAge = true
				pkgReq.RequiredAge = claim
			}
		}
		reqs[id] = pkgReq
	}
	return targeting.RejectByVerifiedIdentity(reqs, verified)
}
