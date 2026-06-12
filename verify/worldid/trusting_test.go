package worldid

import (
	"context"
	"testing"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// attWithResponses carries a proof whose responses[] self-report a nullifier and
// the proof_of_human credential — the shape TrustingVerifier reads.
func attWithResponses() *tmproto.Attestation {
	return &tmproto.Attestation{
		Scheme:         "world_id_v4",
		RelyingPartyID: "rp-1",
		SignalBinding:  "binding",
		Proof: map[string]any{
			"responses": []any{
				map[string]any{"nullifier": "0xNULL", "identifier": "proof_of_human"},
			},
		},
	}
}

func TestTrustingVerify_DerivesFromProofWithoutNetwork(t *testing.T) {
	got, err := NewTrustingVerifier().Verify(context.Background(), attWithResponses(), vctx())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Nullifier != "0xNULL" {
		t.Errorf("nullifier = %q, want 0xNULL", got.Nullifier)
	}
	if got.RelyingPartyID != "rp-1" {
		t.Errorf("relying_party_id = %q, want rp-1", got.RelyingPartyID)
	}
	if _, ok := got.Claims[tmproto.AttestationClaimUniqueHuman]; !ok {
		t.Errorf("want unique_human in claims, got %v", got.Claims)
	}
}

func TestTrustingVerify_RejectsEmptyProof(t *testing.T) {
	att := attWithResponses()
	att.Proof = nil
	if _, err := NewTrustingVerifier().Verify(context.Background(), att, vctx()); err == nil {
		t.Fatal("expected error when attestation has no proof")
	}
}

func TestTrustingVerify_RejectsEmptyExpectedRP(t *testing.T) {
	if _, err := NewTrustingVerifier().Verify(context.Background(), attWithResponses(), targeting.VerifyContext{}); err == nil {
		t.Fatal("expected error with no expected relying_party_id")
	}
}

func TestTrustingVerify_RejectsNoRecognisedClaim(t *testing.T) {
	att := attWithResponses()
	att.Proof = map[string]any{
		"responses": []any{map[string]any{"nullifier": "0xNULL", "identifier": "something_unknown"}},
	}
	if _, err := NewTrustingVerifier().Verify(context.Background(), att, vctx()); err == nil {
		t.Fatal("expected error when no recognised claim is reported")
	}
}
