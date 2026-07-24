package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/bench/context-perf/internal/signkey"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestSnapshotVerifiesLoadgenSignature proves the end-to-end contract the
// bench relies on: the mock tmpregistry serves the loadgen's public JWK in
// a shape that tmproto.RemoteKeyStore accepts, and a signature the loadgen
// produces with the shared private key verifies via that keystore.
func TestSnapshotVerifiesLoadgenSignature(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "signer.json")
	kid := "bench-signer-1"
	kp, err := signkey.LoadOrGenerate(keyPath, kid)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	jwk := tmproto.PublicSigningKey(kp.Kid, kp.PublicKey)
	jwk.AdcpUse = "request-signing"
	jwk.IssuedAt = kp.IssuedAt.Unix()
	snap := snapshot{
		Properties: []snapshotProperty{{
			PropertyID:  "bench-property",
			PropertyRID: "bench:property:1",
			SigningKeys: []tmproto.SigningKey{jwk},
		}},
	}
	payload, err := json.Marshal(&snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	ks, err := tmproto.NewRemoteKeyStore(tmproto.RemoteKeyStoreOptions{
		URL:                 srv.URL + "/registry/snapshot",
		AllowInsecureScheme: true,
	})
	if err != nil {
		t.Fatalf("NewRemoteKeyStore: %v", err)
	}
	if _, err := ks.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := ks.LookupKey(kid); !ok {
		t.Fatalf("keystore missing kid %q after snapshot fetch", kid)
	}

	signer, err := tmproto.NewSigner(kp.Kid, kp.PrivateKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	req := &tmproto.ContextMatchRequest{
		AdcpVersion:    "3.1",
		Type:           tmproto.TypeContextMatchRequest,
		RequestID:      "req-1",
		PropertyRID:    "prop:1",
		PropertyID:     "prop-id-1",
		PropertyType:   tmproto.PropertyType("website"),
		PlacementID:    "placement-1",
		SellerAgentURL: "https://seller.perf.local/agent",
		PackageIDs:     []string{"pkg-00001"},
	}
	provider := "http://context-agent:8081/context"
	epoch := tmproto.CurrentEpoch()
	sig := signer.SignContextMatch(req, provider, epoch)

	if err := tmproto.VerifyContextMatch(req, provider, sig, kid, ks, time.Now()); err != nil {
		t.Fatalf("VerifyContextMatch: %v", err)
	}
}
