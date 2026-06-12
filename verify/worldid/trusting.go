package worldid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TrustingVerifier derives the verified identity from the proof's own
// self-reported responses WITHOUT validating the World ID proof or contacting
// World's backend. It exists to exercise the identity-match and TMPX path
// end-to-end without a live verifier; because it trusts the sender, it offers
// no Sybil resistance and no proof-of-personhood guarantee. A deployment wires
// it in only behind an explicit, default-off switch and never serves production
// traffic with it.
//
// It reads the same nullifier and credential fields Verifier reads from World's
// verify response, but from the inbound proof rather than an authoritative one.
type TrustingVerifier struct{}

// NewTrustingVerifier returns a TrustingVerifier.
func NewTrustingVerifier() *TrustingVerifier { return &TrustingVerifier{} }

var _ targeting.AttestationVerifier = (*TrustingVerifier)(nil)

// Verify returns the nullifier and claims the proof reports about itself,
// scoped to vctx.ExpectedRelyingPartyID, performing no cryptographic
// validation. No proof, no nullifier, no recognised claim, or no expected
// relying party returns an error, which the caller treats as "no attestation".
func (TrustingVerifier) Verify(_ context.Context, att *tmproto.Attestation, vctx targeting.VerifyContext) (targeting.VerifiedIdentity, error) {
	var zero targeting.VerifiedIdentity
	if att == nil || len(att.Proof) == 0 {
		return zero, errors.New("worldid: attestation carries no proof material")
	}
	if vctx.ExpectedRelyingPartyID == "" {
		return zero, errors.New("worldid: no expected relying_party_id to scope to")
	}

	// The proof's responses[] carry the nullifier and credential identifier in
	// the same shape as World's verify response — read them without validating.
	raw, err := json.Marshal(att.Proof)
	if err != nil {
		return zero, fmt.Errorf("worldid: marshal proof: %w", err)
	}
	var vr verifyResponse
	if err := json.Unmarshal(raw, &vr); err != nil {
		return zero, fmt.Errorf("worldid: decode proof: %w", err)
	}
	nullifier := vr.nullifier()
	if nullifier == "" {
		return zero, errors.New("worldid: proof carried no nullifier")
	}
	claims := vr.claims()
	if len(claims) == 0 {
		return zero, errors.New("worldid: proof reported no recognised claim")
	}

	return targeting.VerifiedIdentity{
		Nullifier:      nullifier,
		RelyingPartyID: vctx.ExpectedRelyingPartyID,
		Claims:         claims,
	}, nil
}
