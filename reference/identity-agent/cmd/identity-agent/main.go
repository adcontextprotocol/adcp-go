package main

import (
	"context"
	"crypto/ecdh"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/glidestore"
	"github.com/adcontextprotocol/adcp-go/targeting/prommetrics"
	"github.com/adcontextprotocol/adcp-go/tmproto"

	glide "github.com/valkey-io/valkey-glide/go/v2"
	"github.com/valkey-io/valkey-glide/go/v2/config"
)

var version = "dev"

func main() {
	addr := flag.String("addr", "", "Listen address")
	valkeyAddr := flag.String("valkey-addr", "", "Valkey address (host:port). Falls back to in-memory store if empty or unreachable.")
	registryURL := flag.String("registry-url", "", "URL of the router's /registry/snapshot endpoint for signing-key discovery")
	allowUnsigned := flag.Bool("allow-unsigned", false, "Accept /tmp/identity requests without a TMP signature. Default is deny — TMP signing is normative in the spec. Use only for migration windows or local dev.")
	ownEndpointURL := flag.String("own-endpoint-url", "", "This provider's registered endpoint URL (must match the router's provider registration). Required when --registry-url is set.")
	tmpxKid := flag.String("tmpx-kid", "", "Buyer-cluster TMPX recipient kid (≤8 chars). Enables TMPX token generation when set together with --tmpx-pubkey-path.")
	tmpxPubKey := flag.String("tmpx-pubkey-path", "", "Path to the TMPX recipient X25519 public key (32 bytes, hex- or base64url-encoded).")
	tmpxCountry := flag.String("tmpx-country", "", "ISO 3166-1 alpha-2 country code stamped into the TMPX header. Required when TMPX is enabled.")
	flag.Parse()

	flagSet := setFlags()

	listenAddr := resolveAddr(*addr)
	storeAddr := resolveValkeyAddr(*valkeyAddr)
	regURL := resolveString(*registryURL, flagSet["registry-url"], "TMP_IDENTITY_REGISTRY_URL")
	ownURL := resolveString(*ownEndpointURL, flagSet["own-endpoint-url"], "TMP_IDENTITY_ENDPOINT_URL")
	if !flagSet["allow-unsigned"] {
		if envValue, ok := os.LookupEnv("TMP_IDENTITY_ALLOW_UNSIGNED"); ok {
			*allowUnsigned = envValue == "1" || envValue == "true"
		}
	}
	requireSig := !*allowUnsigned

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	metrics := prommetrics.New()
	store := initStore(storeAddr)
	resolved, err := seedConfigs(store)
	if err != nil {
		slog.Error("seed configs failed", "error", err)
		os.Exit(1)
	}

	engine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "reference-identity-agent",
		Store:      store,
		Metrics:    metrics,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-display-0041"},
			{PackageID: "pkg-display-0042"},
			{PackageID: "pkg-native-0078"},
		},
	})

	keystoreCtx, keystoreCancel := context.WithCancel(context.Background())
	defer keystoreCancel()
	keystore, ksErr := buildKeyStore(keystoreCtx, regURL, requireSig)
	if ksErr != nil {
		slog.Error("keystore init failed", "error", ksErr)
		os.Exit(1)
	}
	if requireSig && ownURL == "" {
		slog.Error("--own-endpoint-url is required when signature verification is enabled (default)")
		os.Exit(1)
	}
	if !requireSig {
		slog.Warn("/tmp/identity accepts unsigned requests — TMP signing should be required in production")
	}

	tmpxCfg, err := loadTmpxConfig(
		resolveString(*tmpxKid, flagSet["tmpx-kid"], "TMP_IDENTITY_TMPX_KID"),
		resolveString(*tmpxPubKey, flagSet["tmpx-pubkey-path"], "TMP_IDENTITY_TMPX_PUBKEY_PATH"),
		resolveString(*tmpxCountry, flagSet["tmpx-country"], "TMP_IDENTITY_TMPX_COUNTRY"),
	)
	if err != nil {
		slog.Error("tmpx config load failed", "error", err)
		os.Exit(1)
	}
	if tmpxCfg != nil {
		ack := os.Getenv("TMP_IDENTITY_TMPX_REFERENCE_STUB_ACK")
		if ack != "1" && ack != "true" {
			slog.Error("TMPX is configured but the reference identity-agent uses a SHA-512 stub for string→binary token decoding that is NOT interoperable with any real buyer master. Set TMP_IDENTITY_TMPX_REFERENCE_STUB_ACK=1 to acknowledge and start.")
			os.Exit(1)
		}
		slog.Warn("TMPX generation enabled with reference SHA-512 stub — buyer masters will not be able to decode these tokens",
			"kid", tmpxCfg.Kid, "country", tmpxCfg.Country)
	}

	mux := http.NewServeMux()

	identityHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: "failed to read request body"})
			return
		}
		var req tmproto.IdentityMatchRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: "request body is not valid JSON"})
			return
		}
		if err := tmproto.ValidateIdentityRequest(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{RequestID: req.RequestID, Code: tmproto.ErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		result, err := engine.EvaluateIdentityResolved(r.Context(), resolved, &req)
		if err != nil {
			slog.Error("EvaluateIdentityResolved failed", "request_id", req.RequestID, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{RequestID: req.RequestID, Code: tmproto.ErrorCodeInternalError, Message: "internal error"})
			return
		}
		var eligible []string
		for _, e := range result.Eligibility {
			if e.Eligible {
				eligible = append(eligible, e.PackageID)
			}
		}
		resp := &tmproto.IdentityMatchResponse{
			RequestID:          result.RequestID,
			EligiblePackageIDs: eligible,
			TTLSec:             60,
		}
		if tmpxCfg != nil && len(eligible) > 0 {
			if token, terr := buildTmpxToken(tmpxCfg, req.Identities); terr != nil {
				slog.Warn("tmpx generation failed, response will omit tmpx", "request_id", req.RequestID, "error", terr)
			} else {
				resp.Tmpx = token
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		slog.Debug("identity match", "request_id", req.RequestID, "packages", len(req.PackageIDs), "latency_ms", time.Since(start).Milliseconds())
	})

	// Wrap with TMP signature verification when configured. Without a
	// keystore, signed requests still pass through unverified — operators
	// who care about authenticated fan-outs MUST set --registry-url and
	// --require-signature (or TMP_IDENTITY_REQUIRE_SIGNATURE=1).
	if keystore != nil {
		mux.Handle("POST /tmp/identity", tmproto.VerifyIdentityMatchHandler(identityHandler, tmproto.VerifyOptions{
			KeyStore:         keystore,
			OwnEndpointURL:   ownURL,
			RequireSignature: requireSig,
		}))
	} else {
		mux.Handle("POST /tmp/identity", identityHandler)
	}

	mux.Handle("GET /metrics", metrics.Registry.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
		})
	})

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	slog.Info("Identity Agent starting", "addr", listenAddr, "version", version)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("listen error", "error", err)
		os.Exit(1)
	}
}

func resolveAddr(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("TMP_IDENTITY_ADDR"); env != "" {
		return env
	}
	return ":8082"
}

func resolveValkeyAddr(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("TMP_IDENTITY_VALKEY_ADDR")
}

// initStore connects to Valkey when an address is configured, otherwise falls
// back to the in-memory MockStore. Production agents that share state between
// the targeting engine and fcap.Service should pass the same backend (a
// glidestore.Store satisfies both interfaces) — this reference doesn't
// exercise that path.
func initStore(valkeyAddr string) targeting.Store {
	if valkeyAddr == "" {
		slog.Info("No Valkey address configured, using in-memory store")
		return targeting.NewMockStore()
	}

	host, port, ok := splitHostPort(valkeyAddr)
	if !ok {
		slog.Warn("Invalid Valkey address, falling back to in-memory store", "addr", valkeyAddr)
		return targeting.NewMockStore()
	}

	cfg := config.NewClientConfiguration().WithAddress(&config.NodeAddress{Host: host, Port: port})
	client, err := glide.NewClient(cfg)
	if err != nil {
		slog.Warn("Cannot connect to Valkey, falling back to in-memory store", "addr", valkeyAddr, "error", err)
		return targeting.NewMockStore()
	}

	// Verify reachability with a short PING.
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Ping(pingCtx); err != nil {
		slog.Warn("Cannot reach Valkey, falling back to in-memory store", "addr", valkeyAddr, "error", err)
		client.Close()
		return targeting.NewMockStore()
	}

	slog.Info("Connected to Valkey", "addr", valkeyAddr)
	return glidestore.New(client)
}

func splitHostPort(addr string) (string, int, bool) {
	idx := strings.LastIndex(addr, ":")
	if idx <= 0 || idx == len(addr)-1 {
		return "", 0, false
	}
	port, err := strconv.Atoi(addr[idx+1:])
	if err != nil {
		return "", 0, false
	}
	return addr[:idx], port, true
}

// seedConfigs pushes reference identity configs into the Store and returns
// the resolved package indexes for identity evaluation. Frequency-cap state
// is no longer seeded — that lives in fcap.Service and is set per-impression.
func seedConfigs(store targeting.Store) (*targeting.ResolvedPackages, error) {
	ctx := context.Background()

	configs := []struct {
		pkgID string
		cfg   targeting.PackageIdentityConfig
	}{
		{"pkg-display-0041", targeting.PackageIdentityConfig{
			TargetSegments: []string{"cooking_enthusiast", "home_improvement"},
		}},
		{"pkg-display-0042", targeting.PackageIdentityConfig{}},
		{"pkg-native-0078", targeting.PackageIdentityConfig{
			TargetSegments: []string{"organic_food"},
		}},
	}
	idConfigs := make(map[string]*targeting.PackageIdentityConfig, len(configs))
	segmentIndex := make(map[string][]string)
	for _, c := range configs {
		if err := targeting.SeedPackageIdentityConfig(ctx, store, c.pkgID, c.cfg); err != nil {
			return nil, fmt.Errorf("seed package config %s: %w", c.pkgID, err)
		}
		cfg := c.cfg
		idConfigs[c.pkgID] = &cfg
		for _, seg := range cfg.TargetSegments {
			segmentIndex[seg] = append(segmentIndex[seg], c.pkgID)
		}
	}

	return &targeting.ResolvedPackages{
		SegmentIndex:    segmentIndex,
		IdentityConfigs: idConfigs,
	}, nil
}

// resolveString picks the configured value for a string flag with the
// precedence flag > env > default. flagSet says whether the flag was passed
// on the command line; only when it wasn't may an env var override.
func resolveString(flagVal string, flagSet bool, envName string) string {
	if flagSet {
		return flagVal
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return flagVal
}

// setFlags returns the set of flag names that were explicitly passed on the
// command line. Used to enforce flag > env > default precedence per AGENTS.md.
func setFlags() map[string]bool {
	out := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { out[f.Name] = true })
	return out
}

// tmpxConfig holds the resolved TMPX recipient settings used to seal tokens
// alongside identity-match responses.
type tmpxConfig struct {
	Kid       string
	Country   string
	PublicKey *ecdh.PublicKey
}

// loadTmpxConfig validates flag inputs and parses the recipient X25519 public
// key from disk. Returns (nil, nil) when TMPX is not configured.
func loadTmpxConfig(kid, pubKeyPath, country string) (*tmpxConfig, error) {
	configured := kid != "" || pubKeyPath != "" || country != ""
	if !configured {
		return nil, nil
	}
	if kid == "" || pubKeyPath == "" || country == "" {
		return nil, errors.New("TMPX requires all three of --tmpx-kid, --tmpx-pubkey-path, --tmpx-country")
	}
	raw, err := os.ReadFile(pubKeyPath) //nolint:gosec // operator-supplied path is the contract
	if err != nil {
		return nil, fmt.Errorf("read TMPX public key: %w", err)
	}
	pkBytes, err := decodeX25519PublicKey(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse TMPX public key at %s: %w", pubKeyPath, err)
	}
	pk, err := tmproto.LoadX25519PublicKey(pkBytes)
	if err != nil {
		return nil, err
	}
	return &tmpxConfig{Kid: kid, Country: country, PublicKey: pk}, nil
}

// decodeX25519PublicKey accepts hex or base64url (no-pad / padded) encoding
// of a 32-byte X25519 public key, with surrounding whitespace tolerated.
func decodeX25519PublicKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("expected 32-byte X25519 public key in hex or base64url")
}

// uidToTmpxTypeID maps spec UID types to TMPX type-ID registry entries.
var uidToTmpxTypeID = map[tmproto.UIDType]tmproto.TmpxTypeID{
	tmproto.UIDTypeUID2:                tmproto.TmpxTypeUID2,
	tmproto.UIDTypeEUID:                tmproto.TmpxTypeEUID,
	tmproto.UIDTypeID5:                 tmproto.TmpxTypeID5,
	tmproto.UIDTypeRampID:              tmproto.TmpxTypeRampID,
	tmproto.UIDTypeRampIDDerived:       tmproto.TmpxTypeRampIDDerived,
	tmproto.UIDTypeMAID:                tmproto.TmpxTypeMAID,
	tmproto.UIDTypePairID:              tmproto.TmpxTypePairID,
	tmproto.UIDTypeHashedEmail:         tmproto.TmpxTypeHashedEmail,
	tmproto.UIDTypePublisherFirstParty: tmproto.TmpxTypePublisherFirstParty,
}

// buildTmpxToken seals an HPKE TMPX token containing the resolved identities.
// Identities whose UIDType has no TMPX type-ID mapping are dropped silently
// per the spec's forward-compatibility rule (unknown types skipped).
//
// The string→binary conversion in stubBinaryToken is a reference stub —
// real buyer deployments decode UID2/RampID/etc. according to the source
// graph's encoding. Tokens produced here are not interoperable with a real
// buyer master.
func buildTmpxToken(cfg *tmpxConfig, ids []tmproto.IdentityToken) (string, error) {
	entries := make([]tmproto.TmpxEntry, 0, len(ids))
	for _, id := range ids {
		typeID, ok := uidToTmpxTypeID[id.UIDType]
		if !ok {
			continue
		}
		bin, err := stubBinaryToken(typeID, id.UserToken)
		if err != nil {
			return "", err
		}
		entries = append(entries, tmproto.TmpxEntry{TypeID: typeID, Token: bin})
	}
	if len(entries) == 0 {
		return "", nil
	}
	plaintext, err := tmproto.EncodeTmpxPlaintext(cfg.Country, entries, time.Now())
	if err != nil {
		return "", err
	}
	return tmproto.SealTmpx(tmproto.TmpxRecipient{Kid: cfg.Kid, PublicKey: cfg.PublicKey}, nil, plaintext)
}

// stubBinaryToken converts a string user_token to the binary representation
// TMPX expects for the given type ID. Reference impl only: hashes the source
// string with SHA-512 and truncates to the spec-required byte length. Real
// buyer deployments decode tokens per source-graph encoding.
func stubBinaryToken(typeID tmproto.TmpxTypeID, token string) ([]byte, error) {
	size, ok := tmproto.TmpxTokenSize(typeID)
	if !ok {
		return nil, fmt.Errorf("unknown TMPX type id %d", typeID)
	}
	h := sha512.Sum512([]byte(token))
	out := make([]byte, size)
	copy(out, h[:size])
	return out, nil
}

// buildKeyStore constructs a tmproto.KeyStore from the configured registry
// URL. Returns (nil, nil) when no registry URL is set and signature
// verification is not required — the agent then accepts unsigned requests.
//
// runCtx governs the long-lived background refresh goroutine; cancel it
// during shutdown to drain the goroutine. The synchronous initial fetch is
// bounded to 10 seconds independently.
func buildKeyStore(runCtx context.Context, registryURL string, requireSignature bool) (tmproto.KeyStore, error) {
	if registryURL == "" {
		if requireSignature {
			return nil, errors.New("--registry-url (or TMP_IDENTITY_REGISTRY_URL) is required for signature verification (default). Pass --allow-unsigned to opt out.")
		}
		return nil, nil
	}
	ks, err := tmproto.NewRemoteKeyStore(tmproto.RemoteKeyStoreOptions{URL: registryURL})
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(runCtx, 10*time.Second)
	defer cancel()
	if _, err := ks.Refresh(fetchCtx); err != nil {
		return nil, fmt.Errorf("initial registry fetch from %s: %w", registryURL, err)
	}
	go func() {
		if err := ks.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("registry keystore Run terminated", "url", registryURL, "error", err)
		}
	}()
	return ks, nil
}
