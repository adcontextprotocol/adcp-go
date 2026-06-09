package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestEndToEnd_OpenVerifyGate drives the real verified-identity receiver
// pipeline: an HPKE-sealed credential carrying a World ID proof is opened,
// verified against a mocked World backend, and gated. It asserts verify-before-
// trust — World confirms only unique_human, so the sender-asserted age_over_21
// is NOT trusted and the 21+ package stays ineligible while the open package
// passes.
func TestEndToEnd_OpenVerifyGate(t *testing.T) {
	// Mock World: confirms proof-of-human (→ unique_human) but no age.
	world := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/verify/rp-1" {
			t.Errorf("verify path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":   true,
			"responses": []map[string]any{{"nullifier": "0xNULL", "identifier": "proof_of_human"}},
		})
	}))
	defer world.Close()

	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{
		rpID:         "rp-1",
		audienceKID:  "kid-1",
		sellerURL:    "seller.example",
		worldBaseURL: world.URL,
		recipientKey: sk,
	}
	svc, err := buildService(cfg)
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}

	// Seal an attestation (the network/publisher's job) carrying a World ID
	// proof, asserting age_over_21 — which must be ignored since World won't
	// confirm it.
	att := tmproto.Attestation{
		Issuer:         map[string]any{"domain": "world.org"},
		Scheme:         "world_id_v4",
		RelyingPartyID: cfg.rpID,
		SignalBinding:  "r1",
		Claims:         []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman, tmproto.AttestationClaimAgeOver21},
		Proof:          map[string]any{"proof": "0xPROOF", "merkle_root": "0xROOT", "verification_level": "orb"},
	}
	pt, err := json.Marshal(att)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := tmproto.SealTmpx(tmproto.TmpxRecipient{Kid: cfg.audienceKID, PublicKey: sk.PublicKey()}, nil, pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	req := &tmproto.IdentityMatchRequest{
		RequestID:         "r1",
		SellerAgentURL:    cfg.sellerURL,
		PackageIDs:        []string{pkgAlcohol, pkgGeneral},
		Identities:        []tmproto.IdentityToken{{UserToken: "u1", UIDType: tmproto.UIDTypeID5}},
		SealedCredentials: []tmproto.SealedCredential{{AudienceKID: cfg.audienceKID, Payload: wire}},
	}

	got := map[string]bool{}
	for _, e := range svc.Evaluate(context.Background(), req).Eligibility {
		got[e.PackageID] = e.Eligible
	}

	if !got[pkgGeneral] {
		t.Errorf("%s should be eligible (verified human, no age requirement)", pkgGeneral)
	}
	if got[pkgAlcohol] {
		t.Errorf("%s must be INELIGIBLE: World confirmed only unique_human; sender-asserted age_over_21 is not trusted", pkgAlcohol)
	}
}
