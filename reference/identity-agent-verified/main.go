// Command identity-agent-verified is a runnable reference buyer agent that
// exercises the TMP verified-identity receiver path end to end on the shipped
// adcp-go code: it opens HPKE-sealed credentials, verifies the World ID proof
// inside (via the worldid.Verifier), and gates package eligibility — fail
// closed, with the relying-party-scoped nullifier as the frequency-cap key.
//
// It is a reference/demo, not production: stores are in-memory, the recipient
// HPKE key is generated at boot unless AGENT_RECIPIENT_KEY is set, and the age
// policy is a static map (production resolves it via the AdCP Policy Registry).
//
// Endpoints:
//
//	GET  /recipient  → {audience_kid, public_key, rp_id, seller_agent_url}
//	                   the sender uses this to seal a credential to this agent.
//	POST /seal       → demo helper: {proof, request_id, claims?} → builds an
//	                   attestation around a World ID proof and HPKE-seals it,
//	                   returning a sealed_credentials entry. (In production the
//	                   network/publisher seals; the agent never does.)
//	POST /identity   → an IdentityMatchRequest (carrying sealed_credentials);
//	                   runs the real verified-identity eligibility pipeline.
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/adcontextprotocol/adcp-go/reference/identity-agent-verified/worldid"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/identityagent"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Demo packages: one age-gated (21+), one open. The static age resolver gates
// pkg_alcohol; pkg_general has no requirement.
const (
	pkgAlcohol = "pkg_alcohol"
	pkgGeneral = "pkg_general"
)

func main() {
	cfg := loadConfig()

	svc, err := buildService(cfg)
	if err != nil {
		log.Fatalf("build service: %v", err)
	}

	a := &agent{cfg: cfg, svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /recipient", a.handleRecipient)
	mux.HandleFunc("POST /seal", a.handleSeal)
	mux.HandleFunc("POST /identity", a.handleIdentity)

	log.Printf("identity-agent-verified listening on %s (rp_id=%s, audience_kid=%s, world=%s)",
		cfg.addr, cfg.rpID, cfg.audienceKID, cfg.worldBaseURL)
	srv := &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type config struct {
	addr         string
	rpID         string
	audienceKID  string
	sellerURL    string
	worldBaseURL string
	worldAPIKey  string
	recipientKey *ecdh.PrivateKey
}

func loadConfig() config {
	c := config{
		addr:         envOr("AGENT_ADDR", ":8799"),
		rpID:         envOr("RP_ID", ""),
		audienceKID:  envOr("AUDIENCE_KID", "kid-1"),
		sellerURL:    envOr("SELLER_AGENT_URL", "seller.example"),
		worldBaseURL: envOr("WORLD_VERIFY_BASE_URL", worldid.DefaultBaseURL),
		worldAPIKey:  os.Getenv("WORLD_API_KEY"),
	}
	if c.rpID == "" {
		log.Fatal("RP_ID is required (the relying party this agent acts as; must match the widget's rp_id)")
	}
	// Recipient HPKE key: load 32 raw hex bytes from AGENT_RECIPIENT_KEY, or
	// generate an ephemeral one for the demo. Never logged.
	if raw := os.Getenv("AGENT_RECIPIENT_KEY"); raw != "" {
		b, err := hex.DecodeString(raw)
		if err != nil {
			log.Fatalf("AGENT_RECIPIENT_KEY must be hex: %v", err)
		}
		sk, err := tmproto.LoadX25519PrivateKey(b)
		if err != nil {
			log.Fatalf("AGENT_RECIPIENT_KEY: %v", err)
		}
		c.recipientKey = sk
	} else {
		sk, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			log.Fatalf("generate recipient key: %v", err)
		}
		c.recipientKey = sk
		log.Printf("AGENT_RECIPIENT_KEY unset — generated an ephemeral recipient key for this run")
	}
	return c
}

func buildService(cfg config) (*identityagent.Service, error) {
	configSvc, err := identityconfig.New(&memSource{entries: []identityconfig.Entry{
		{Key: identityconfig.Key{SellerAgentURL: cfg.sellerURL, PackageID: pkgAlcohol}},
		{Key: identityconfig.Key{SellerAgentURL: cfg.sellerURL, PackageID: pkgGeneral}},
	}}, time.Minute)
	if err != nil {
		return nil, err
	}
	if err := configSvc.Start(context.Background()); err != nil {
		return nil, err
	}

	return identityagent.NewService(identityagent.ServiceConfig{
		Engine:          targeting.NewIdentityEngine(targeting.IdentityEngineConfig{}),
		FCap:            fcap.New(fcap.NewMockStore()),
		ConfigService:   configSvc,
		FCapTimeout:     50 * time.Millisecond,
		AudienceTimeout: 50 * time.Millisecond,
		Verifier:        worldid.New(worldid.WithBaseURL(cfg.worldBaseURL), worldid.WithAPIKey(cfg.worldAPIKey)),
		RecipientKeys: map[string]identityagent.RecipientKey{
			cfg.audienceKID: {PrivateKey: cfg.recipientKey, RelyingPartyID: cfg.rpID},
		},
		AgeResolver: staticAgeResolver{pkgAlcohol: tmproto.AttestationClaimAgeOver21},
	})
}

type agent struct {
	cfg config
	svc *identityagent.Service
}

func (a *agent) handleRecipient(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"audience_kid":     a.cfg.audienceKID,
		"public_key":       base64.RawURLEncoding.EncodeToString(a.cfg.recipientKey.PublicKey().Bytes()),
		"rp_id":            a.cfg.rpID,
		"seller_agent_url": a.cfg.sellerURL,
		"packages":         []string{pkgAlcohol, pkgGeneral},
	})
}

// handleSeal is a demo convenience: it wraps a World ID proof in an attestation
// and HPKE-seals it to this agent's own recipient key, returning a
// sealed_credentials entry the caller can put on an IdentityMatchRequest. In
// production the network/publisher does this; the agent never seals.
func (a *agent) handleSeal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Proof     map[string]any `json:"proof"`
		RequestID string         `json:"request_id"`
		Claims    []string       `json:"claims"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if len(in.Proof) == 0 || in.RequestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "proof and request_id are required"})
		return
	}
	claims := make([]tmproto.AttestationClaim, 0, len(in.Claims))
	for _, c := range in.Claims {
		claims = append(claims, tmproto.AttestationClaim(c))
	}
	att := tmproto.Attestation{
		Issuer:         map[string]any{"domain": "world.org"},
		Scheme:         "world_id_v4",
		RelyingPartyID: a.cfg.rpID,
		SignalBinding:  in.RequestID, // demo: bind to the request_id (non-empty ⇒ passes the SDK pre-check)
		Claims:         claims,       // asserted only; the verifier trusts World, not this
		Proof:          in.Proof,
	}
	pt, err := json.Marshal(att)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "marshal attestation"})
		return
	}
	wire, err := tmproto.SealTmpx(tmproto.TmpxRecipient{Kid: a.cfg.audienceKID, PublicKey: a.cfg.recipientKey.PublicKey()}, nil, pt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "seal"})
		return
	}
	writeJSON(w, http.StatusOK, tmproto.SealedCredential{AudienceKID: a.cfg.audienceKID, Payload: wire})
}

func (a *agent) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var req tmproto.IdentityMatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	result := a.svc.Evaluate(r.Context(), &req)
	writeJSON(w, http.StatusOK, result)
}

// memSource is a fixed in-memory identityconfig.Source for the demo.
type memSource struct{ entries []identityconfig.Entry }

func (s *memSource) LoadAll(context.Context) (identityconfig.Snapshot, error) {
	return identityconfig.Snapshot{Configs: s.entries, LastUpdatedAt: time.Unix(0, 0)}, nil
}
func (s *memSource) LoadUpdatedAfter(context.Context, time.Time) (identityconfig.Delta, error) {
	return identityconfig.Delta{LastUpdatedAt: time.Unix(0, 0)}, nil
}

// staticAgeResolver maps a package id to its required age claim (demo stand-in
// for the AdCP Policy Registry resolution).
type staticAgeResolver map[string]tmproto.AttestationClaim

func (m staticAgeResolver) ResolveRequiredAge(_ context.Context, pkgID, _ string) (tmproto.AttestationClaim, bool) {
	c, ok := m[pkgID]
	return c, ok
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
