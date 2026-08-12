// Command bootstrap materializes the two files the router needs before it
// can start, plus the public half of its signing key for the registry stub.
//
// Written into the shared volume mounted at -out:
//
//	router-config.json       everything the router reads, pointed at by
//	                         TMP_ROUTER_CONFIG — the provider list plus the
//	                         health, cache, signing and registry-feed sections
//	router-signing-key.pem   PKCS#8 Ed25519 private key, reached through the
//	                         config's signing.private_key_path
//	router-signing-key.jwk   public JWK, for the registry stub's authorization rows
//
// Generating the config rather than committing it is deliberate: the provider
// endpoints, property RIDs, uid types and country all come from the fixture
// package, so the router's view of the stack cannot drift from what the
// seeder wrote and the verifier asserts.
//
// Compose points only TMP_ROUTER_CONFIG at this directory; the key path travels
// inside the generated config, as signing.private_key_path.
//
// The router image runs as distroless nonroot (uid 65532), so the key is
// written world-readable. That is safe here and only here: the key is
// generated fresh for one hermetic test network and never leaves it.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/adcontextprotocol/adcp-go/e2e/stack/internal/fixture"
	"github.com/adcontextprotocol/adcp-go/router"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// File names inside the shared volume. The registry stub reads the JWK from the
// same directory.
const (
	KeyFileName    = "router-signing-key.pem"
	ConfigFileName = "router-config.json"
	JWKFileName    = "router-signing-key.jwk"
)

// sharedMountPath is where the router mounts the shared volume. The generated
// config embeds an absolute key path, so this has to match the router's mount
// point rather than bootstrap's own -out flag: the compose file mounts the
// same volume at the same path in both containers.
const sharedMountPath = "/shared"

// providerTimeout is the per-provider budget the router allows. Well above
// what either agent needs on a healthy machine, and below the latency
// budget so the config passes validation.
const providerTimeoutMs = 1500

// latencyBudgetMs bounds the whole fan-out. Deliberately generous: the
// stack asserts on correctness, and a laptop under docker has no business
// being held to a production 50ms budget.
const latencyBudgetMs = 2000

func main() {
	outDir := flag.String("out", "/shared", "directory to write the router key, JWK and config into")
	flag.Parse()

	// 0755, not 0750: the router container runs as a different uid than this
	// one and has to traverse the directory to read the key and config.
	if err := os.MkdirAll(*outDir, 0o755); err != nil { //nolint:gosec // see above
		log.Fatalf("bootstrap: create %s: %v", *outDir, err)
	}

	// Reuse an existing key rather than minting a new one. bootstrap is a
	// dependency of both the router and the registry stub, so compose may
	// invoke it more than once in a single stack; a second run that replaced
	// the key would leave the stub serving a public JWK that no longer matches
	// the private key the router signs with, and every signature would fail.
	// A fresh key per run comes from `down -v` clearing the volume, not from
	// this process being run exactly once.
	keyPath := filepath.Join(*outDir, KeyFileName)
	priv, reused, err := loadOrCreatePrivateKey(keyPath)
	if err != nil {
		log.Fatalf("bootstrap: signing key at %s: %v", keyPath, err)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		log.Fatalf("bootstrap: signing key at %s is not Ed25519", keyPath)
	}

	jwk := tmproto.PublicSigningKey(fixture.RouterSigningKID, pub)
	jwk.AdcpUse = "request-signing"
	jwk.IssuedAt = time.Now().Unix()
	jwkPath := filepath.Join(*outDir, JWKFileName)
	if err := writeJSON(jwkPath, jwk); err != nil {
		log.Fatalf("bootstrap: write %s: %v", jwkPath, err)
	}

	configPath := filepath.Join(*outDir, ConfigFileName)
	if err := writeJSON(configPath, routerConfig()); err != nil {
		log.Fatalf("bootstrap: write %s: %v", configPath, err)
	}

	origin := "generated"
	if reused {
		origin = "reused"
	}
	log.Printf("bootstrap: %s %s; wrote %s, %s (kid=%s)",
		origin, keyPath, jwkPath, configPath, fixture.RouterSigningKID)
}

// loadOrCreatePrivateKey returns the Ed25519 key at path, generating and
// persisting one when the file is absent. The bool reports whether an existing
// key was reused.
func loadOrCreatePrivateKey(path string) (ed25519.PrivateKey, bool, error) {
	pemBytes, err := os.ReadFile(path) //nolint:gosec // path is derived from a flag, not request input
	switch {
	case err == nil:
		priv, err := tmproto.LoadEd25519PrivateKeyPEM(pemBytes)
		if err != nil {
			return nil, false, fmt.Errorf("parse existing key: %w", err)
		}
		return priv, true, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, false, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate: %w", err)
	}
	if err := writePrivateKeyPEM(path, priv); err != nil {
		return nil, false, err
	}
	return priv, false, nil
}

// routerConfig builds the router's config file.
//
// Loading a config file replaces the router's built-in defaults wholesale,
// so every field that has a harmful zero value is set explicitly:
// addr (empty binds :80), shutdown.drain_seconds (0 cuts off in-flight
// requests instead of draining), and the two health fields (0/0 reopens the
// circuit the instant it trips, so a dead provider never leaves the
// fan-out).
//
// Signing and registry settings go in the file rather than in compose
// environment variables so the property RIDs, the feed URL and the token all
// keep coming from the fixture package. The one router setting the compose
// file does supply through the environment is the feed poll interval — a
// tuning knob, and the layering it proves (env over file) is the documented
// precedence rule.
func routerConfig() router.ServerConfig {
	uidTypes := []string{string(fixture.UIDType)}
	propertyRIDs := make([]string, 0, len(fixture.Properties))
	for _, p := range fixture.Properties {
		propertyRIDs = append(propertyRIDs, p.PropertyRID)
	}
	return router.ServerConfig{
		Addr:            fixture.RouterAddr,
		AdminAddr:       fixture.RouterAdminAddr,
		LatencyBudgetMs: latencyBudgetMs,
		Providers: []router.ProviderConfig{
			{
				ID:           fixture.ContextProviderID,
				Endpoint:     fixture.ContextAgentEndpoint,
				Status:       router.ProviderStatusActive,
				ContextMatch: true,
				WireFormats:  []string{"json"},
				Timeout:      providerTimeoutMs * time.Millisecond,
			},
			{
				ID:            fixture.IdentityProviderID,
				Endpoint:      fixture.IdentityAgentEndpoint,
				Status:        router.ProviderStatusActive,
				IdentityMatch: true,
				WireFormats:   []string{"json"},
				// Required whenever identity_match is true. Both also drive
				// router-side filtering the verifier asserts on.
				Countries: []string{fixture.Country},
				UIDTypes:  uidTypes,
				Timeout:   providerTimeoutMs * time.Millisecond,
			},
		},
		// Two consecutive failures open the circuit and a short cooldown
		// closes it again, so the provider-health scenario resolves inside
		// the run rather than outliving it.
		Health: router.HealthConfig{FailureThreshold: 2, CooldownSeconds: 5},
		// Poll often enough that a provider coming up after the router is
		// marked healthy within a couple of seconds.
		HealthCheck: router.HealthCheckConfig{IntervalSeconds: 2, TimeoutSeconds: 2},
		Shutdown:    router.ShutdownConfig{DrainSeconds: 5},
		Cache:       router.CacheConfig{DefaultTTLSeconds: 60},
		Signing: router.SigningConfig{
			KeyID:          fixture.RouterSigningKID,
			PrivateKeyPath: filepath.Join(sharedMountPath, KeyFileName),
			// Every registered property, so /registry/snapshot carries the
			// router's key on each RID a request can arrive for.
			PropertyRIDs: propertyRIDs,
		},
		Registry: router.RegistryConfig{
			FeedURL:   fixture.RegistryStubBaseURL,
			FeedToken: fixture.RegistryFeedToken,
		},
	}
}

func writePrivateKeyPEM(path string, priv ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal pkcs#8: %w", err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if block == nil {
		return fmt.Errorf("encode pem")
	}
	return os.WriteFile(path, block, 0o644) //nolint:gosec // see the package comment: distroless nonroot must read it
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // read by the router and the registry stub
}
