// tmpregistry is a static mock of the TMP registry snapshot endpoint that
// the identity-agent / context-agent poll via TMP_REGISTRY_URL. It serves a
// single-property snapshot advertising the loadgen's Ed25519 public key so
// the agent's RemoteKeyStore can resolve X-AdCP-Key-Id → public key on every
// signed request without touching a real registry.
//
// The registry and the loadgen share the private key material through a
// mounted volume (see internal/signkey). On startup this process ensures the
// key file exists — generating one if the volume is empty — so a fresh
// benchmark run is a one-command boot.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/adcontextprotocol/adcp-go/bench/identity-perf/internal/signkey"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

type snapshotProperty struct {
	PropertyID  string               `json:"property_id"`
	PropertyRID string               `json:"property_rid"`
	SigningKeys []tmproto.SigningKey `json:"signing_keys"`
}

type snapshot struct {
	Properties []snapshotProperty `json:"properties"`
}

func main() {
	port := flag.Int("port", envInt("PORT", 9002), "listen port")
	keyPath := flag.String("key-path", envStr("SIGNER_KEY_PATH", "/keys/signer.json"), "shared signer keypair path")
	kid := flag.String("kid", envStr("SIGNER_KID", "bench-signer-1"), "signer kid to generate when the key file is absent")
	propertyID := flag.String("property-id", envStr("PROPERTY_ID", "bench-property"), "property_id advertised on the snapshot")
	propertyRID := flag.String("property-rid", envStr("PROPERTY_RID", "bench:property:1"), "property_rid advertised on the snapshot")
	flag.Parse()

	kp, err := signkey.LoadOrGenerate(*keyPath, *kid)
	if err != nil {
		log.Fatalf("tmpregistry: load or generate keypair at %s: %v", *keyPath, err)
	}
	log.Printf("tmpregistry: signer kid=%s (issued %s)", kp.Kid, kp.IssuedAt.Format(time.RFC3339))

	jwk := tmproto.PublicSigningKey(kp.Kid, kp.PublicKey)
	jwk.AdcpUse = "request-signing"
	jwk.IssuedAt = kp.IssuedAt.Unix()

	snap := snapshot{
		Properties: []snapshotProperty{{
			PropertyID:  *propertyID,
			PropertyRID: *propertyRID,
			SigningKeys: []tmproto.SigningKey{jwk},
		}},
	}
	payload, err := json.Marshal(&snap)
	if err != nil {
		log.Fatalf("tmpregistry: marshal snapshot: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/registry/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("tmpregistry: listening on %s (kid=%s property_rid=%s)", addr, kp.Kid, *propertyRID)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("tmpregistry: serve: %v", err)
	}
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		log.Fatalf("%s=%q is not an integer: %v", name, v, err)
	}
	return n
}
