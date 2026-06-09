package worldid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func attWithProof() *tmproto.Attestation {
	return &tmproto.Attestation{
		Scheme:         "world_id_v4",
		RelyingPartyID: "rp-1",
		SignalBinding:  "binding",
		// Sender asserts age_over_21 — but the verifier must trust ONLY what
		// World confirms, never this.
		Claims: []tmproto.AttestationClaim{tmproto.AttestationClaimAgeOver21},
		Proof:  map[string]any{"proof": "0xPROOF", "merkle_root": "0xROOT", "verification_level": "orb"},
	}
}

func vctx() targeting.VerifyContext {
	return targeting.VerifyContext{RequestID: "r1", ExpectedRelyingPartyID: "rp-1"}
}

// mockWorld returns an httptest server that asserts the rp-scoped path and the
// forwarded proof body, then replies with `resp` and `status`.
func mockWorld(t *testing.T, status int, resp any) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v4/verify/rp-1" {
			t.Errorf("verify path = %q, want /api/v4/verify/rp-1", r.URL.Path)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode forwarded proof: %v", err)
		}
		if got["proof"] != "0xPROOF" {
			t.Errorf("forwarded proof body = %v, want the verbatim att.Proof", got)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestVerify_HappyPath_DerivesClaimsFromWorld(t *testing.T) {
	srv, _ := mockWorld(t, http.StatusOK, map[string]any{
		"success":   true,
		"responses": []map[string]any{{"nullifier": "0xNULL", "identifier": "proof_of_human"}},
	})
	v := New(WithBaseURL(srv.URL))

	got, err := v.Verify(context.Background(), attWithProof(), vctx())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Nullifier != "0xNULL" {
		t.Errorf("nullifier = %q, want 0xNULL", got.Nullifier)
	}
	if got.RelyingPartyID != "rp-1" {
		t.Errorf("relying_party_id = %q, want rp-1", got.RelyingPartyID)
	}
	// World confirmed proof_of_human → unique_human ONLY. The sender's asserted
	// age_over_21 must NOT appear (verify before trust).
	if _, ok := got.Claims[tmproto.AttestationClaimUniqueHuman]; !ok {
		t.Errorf("want unique_human in claims, got %v", got.Claims)
	}
	if _, ok := got.Claims[tmproto.AttestationClaimAgeOver21]; ok {
		t.Errorf("sender-asserted age_over_21 must NOT be trusted; claims=%v", got.Claims)
	}
}

func TestVerify_RejectsOnSuccessFalse(t *testing.T) {
	srv, _ := mockWorld(t, http.StatusOK, map[string]any{"success": false})
	v := New(WithBaseURL(srv.URL))
	if _, err := v.Verify(context.Background(), attWithProof(), vctx()); err == nil {
		t.Fatal("expected error when success=false")
	}
}

func TestVerify_RejectsOnNon200(t *testing.T) {
	srv, _ := mockWorld(t, http.StatusUnauthorized, map[string]any{"error": "invalid_proof"})
	v := New(WithBaseURL(srv.URL))
	if _, err := v.Verify(context.Background(), attWithProof(), vctx()); err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestVerify_RejectsWhenNoNullifier(t *testing.T) {
	srv, _ := mockWorld(t, http.StatusOK, map[string]any{
		"success":   true,
		"responses": []map[string]any{{"identifier": "proof_of_human"}}, // no nullifier
	})
	v := New(WithBaseURL(srv.URL))
	if _, err := v.Verify(context.Background(), attWithProof(), vctx()); err == nil {
		t.Fatal("expected error when response carries no nullifier")
	}
}

func TestVerify_RejectsWhenNoRecognisedClaim(t *testing.T) {
	srv, _ := mockWorld(t, http.StatusOK, map[string]any{
		"success":   true,
		"responses": []map[string]any{{"nullifier": "0xNULL", "identifier": "something_unknown"}},
	})
	v := New(WithBaseURL(srv.URL))
	if _, err := v.Verify(context.Background(), attWithProof(), vctx()); err == nil {
		t.Fatal("expected error when no recognised claim is confirmed")
	}
}

// No HTTP call should happen when there's nothing to verify.
func TestVerify_NoProof_NoCall(t *testing.T) {
	srv, calls := mockWorld(t, http.StatusOK, map[string]any{"success": true})
	v := New(WithBaseURL(srv.URL))
	att := attWithProof()
	att.Proof = nil
	if _, err := v.Verify(context.Background(), att, vctx()); err == nil {
		t.Fatal("expected error when attestation has no proof")
	}
	if *calls != 0 {
		t.Errorf("World verify must not be called when there's no proof; calls=%d", *calls)
	}
}

func TestVerify_RejectsEmptyExpectedRP(t *testing.T) {
	srv, calls := mockWorld(t, http.StatusOK, map[string]any{"success": true})
	v := New(WithBaseURL(srv.URL))
	vc := vctx()
	vc.ExpectedRelyingPartyID = ""
	if _, err := v.Verify(context.Background(), attWithProof(), vc); err == nil {
		t.Fatal("expected error with no expected relying_party_id")
	}
	if *calls != 0 {
		t.Errorf("must not call World with an empty rp_id; calls=%d", *calls)
	}
}
