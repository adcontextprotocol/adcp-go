package main

import (
	"context"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/glidestore"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig/scope3"
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
	tmpxEncryptJWKSURL := flag.String("tmpx-encrypt-jwks-url", "", "URL of the buyer's JWKS endpoint that publishes the active TMPX recipient key (X25519, adcp_use=tmpx-encrypt). Enables TMPX token generation when set.")
	tmpxEncryptJWKSTTL := flag.Duration("tmpx-encrypt-jwks-ttl", 5*time.Minute, "How often to re-poll the TMPX encryption JWKS for key rotation.")
	tmpxCountry := flag.String("tmpx-country", "", "ISO 3166-1 alpha-2 country code stamped into the TMPX header. Required when TMPX is enabled.")
	tmpxPriority := flag.String("tmpx-priority", "", "Comma-separated UID type ordering used to truncate identities when the TMPX wire size would exceed 255 bytes (e.g. 'uid2,rampid,id5'). Spec requires this list be configured before any truncation; without it, an over-budget identity set returns an error.")
	configSourceURL := flag.String("config-source-url", "", "URL of the Scope3 identity-config endpoint. When set, the agent loads PackageIdentityConfig entries from this URL keyed by (seller_agent_url, package_id) and refreshes them periodically.")
	configSourceToken := flag.String("config-source-token", "", "Bearer token sent as Authorization on identity-config requests. Required when --config-source-url is set.")
	configSourceTimeout := flag.Duration("config-source-timeout", 30*time.Second, "Total HTTP timeout for each identity-config request.")
	configRefreshInterval := flag.Duration("config-refresh-interval", 5*time.Minute, "Interval between identity-config delta refreshes.")
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

	cfgURL := resolveString(*configSourceURL, flagSet["config-source-url"], "TMP_IDENTITY_CONFIG_SOURCE_URL")
	cfgToken := resolveString(*configSourceToken, flagSet["config-source-token"], "TMP_IDENTITY_CONFIG_SOURCE_TOKEN")
	if cfgURL == "" {
		slog.Error("--config-source-url is required (Scope3 identity-config endpoint)")
		os.Exit(1)
	}
	if cfgToken == "" {
		slog.Error("--config-source-token is required when --config-source-url is set")
		os.Exit(1)
	}
	cfgSource, err := scope3.New(cfgURL, cfgToken, scope3.WithHTTPTimeout(*configSourceTimeout))
	if err != nil {
		slog.Error("config source init failed", "error", err)
		os.Exit(1)
	}
	configSvc, err := identityconfig.New(cfgSource, *configRefreshInterval,
		identityconfig.WithStartConfig(identityconfig.StartConfig{
			Mode: identityconfig.StartModeRetry,
			Retry: identityconfig.RetryConfig{
				Initial:  time.Second,
				Max:      30 * time.Second,
				Backoff:  identityconfig.BackoffExponential,
				Deadline: 5 * time.Minute,
			},
		}),
		identityconfig.WithLogger(slog.Default()),
	)
	if err != nil {
		slog.Error("config service init failed", "error", err)
		os.Exit(1)
	}
	if err := configSvc.Start(context.Background()); err != nil {
		slog.Error("config service initial load failed", "error", err)
		os.Exit(1)
	}
	defer configSvc.Stop()

	engine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "reference-identity-agent",
		Store:      store,
		Metrics:    metrics,
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
		keystoreCtx,
		resolveString(*tmpxEncryptJWKSURL, flagSet["tmpx-encrypt-jwks-url"], "TMP_IDENTITY_TMPX_ENCRYPT_JWKS_URL"),
		*tmpxEncryptJWKSTTL,
		resolveString(*tmpxCountry, flagSet["tmpx-country"], "TMP_IDENTITY_TMPX_COUNTRY"),
		resolveString(*tmpxPriority, flagSet["tmpx-priority"], "TMP_IDENTITY_TMPX_PRIORITY"),
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
			"country", tmpxCfg.Country)
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
		effectivePkgIDs, idConfigs := identityconfig.ResolveRequest(configSvc, req.SellerAgentURL, req.PackageIDs)
		req.PackageIDs = effectivePkgIDs
		resolved := &targeting.ResolvedPackages{IdentityConfigs: idConfigs}
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
	Country  string
	EncStore tmpxRecipientResolver

	// Priority is the explicit per-spec priority ordering used when the
	// resolved identities exceed the 255-byte wire budget. Entries earlier
	// in the slice rank higher; entries whose UIDType is absent are
	// dropped (the spec requires explicit configuration — arbitrary
	// truncation is forbidden). When Priority is empty, no truncation is
	// performed and an over-budget token is reported as an error.
	Priority []tmproto.UIDType
}

// tmpxRecipientResolver returns the buyer-cluster TMPX recipient at the
// moment of sealing. Backed by tmproto.JWKSStore in production; replaceable
// with a fixed recipient in tests.
type tmpxRecipientResolver interface {
	CurrentEncryptionRecipient() (tmproto.TmpxRecipient, bool)
}

// loadTmpxConfig validates flag inputs and parses the recipient X25519 public
// key from disk. Returns (nil, nil) when TMPX is not configured.
func loadTmpxConfig(runCtx context.Context, jwksURL string, jwksTTL time.Duration, country, priority string) (*tmpxConfig, error) {
	configured := jwksURL != "" || country != "" || priority != ""
	if !configured {
		return nil, nil
	}
	if jwksURL == "" || country == "" {
		return nil, errors.New("TMPX requires --tmpx-encrypt-jwks-url and --tmpx-country")
	}
	store, err := tmproto.NewJWKSStore(tmproto.JWKSStoreOptions{
		URL:             jwksURL,
		RefreshInterval: jwksTTL,
	})
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(runCtx, 10*time.Second)
	defer cancel()
	if err := store.Refresh(fetchCtx); err != nil {
		return nil, fmt.Errorf("initial TMPX JWKS fetch from %s: %w", jwksURL, err)
	}
	if _, ok := store.CurrentEncryptionRecipient(); !ok {
		return nil, fmt.Errorf("TMPX JWKS at %s does not publish an adcp_use=tmpx-encrypt key", jwksURL)
	}
	go func() {
		if err := store.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("TMPX JWKS Run terminated", "url", jwksURL, "error", err)
		}
	}()
	order, err := parseTmpxPriority(priority)
	if err != nil {
		return nil, err
	}
	return &tmpxConfig{Country: country, EncStore: store, Priority: order}, nil
}

// parseTmpxPriority parses a comma-separated list of UID type names into the
// ordered slice used by buildTmpxToken. Whitespace around tokens is tolerated;
// unknown UID types are rejected (a typo would silently drop identities).
func parseTmpxPriority(s string) ([]tmproto.UIDType, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]tmproto.UIDType, 0, len(parts))
	seen := make(map[tmproto.UIDType]bool, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		uid := tmproto.UIDType(name)
		if _, ok := uidToTmpxTypeID[uid]; !ok {
			return nil, fmt.Errorf("--tmpx-priority entry %q is not a TMPX-encodable uid_type", name)
		}
		if seen[uid] {
			return nil, fmt.Errorf("--tmpx-priority entry %q appears more than once", name)
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out, nil
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
// Identities whose UIDType has no TMPX type-ID mapping are dropped per the
// spec's forward-compatibility rule. When cfg.Priority is non-empty, entries
// are sorted by priority and the highest-priority prefix that fits the
// TmpxMaxWireBytes (255) budget is included; identities with a UIDType not in
// the priority list are excluded entirely. When cfg.Priority is empty, the
// spec forbids arbitrary truncation — an over-budget set returns an error.
//
// The string→binary conversion in stubBinaryToken is a reference stub —
// real buyer deployments decode UID2/RampID/etc. according to the source
// graph's encoding. Tokens produced here are not interoperable with a real
// buyer master.
func buildTmpxToken(cfg *tmpxConfig, ids []tmproto.IdentityToken) (string, error) {
	recipient, ok := cfg.EncStore.CurrentEncryptionRecipient()
	if !ok {
		return "", errors.New("no TMPX encryption recipient currently published — buyer JWKS missing adcp_use=tmpx-encrypt key")
	}
	entries, err := selectTmpxEntries(cfg, ids)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	plaintext, err := tmproto.EncodeTmpxPlaintext(cfg.Country, entries, time.Now())
	if err != nil {
		return "", err
	}
	return tmproto.SealTmpx(recipient, nil, plaintext)
}

// selectTmpxEntries returns the ordered TmpxEntries that buildTmpxToken will
// seal: mappable UIDTypes filtered through the operator-configured priority
// list, sorted by priority (highest first), then truncated to fit the
// TmpxMaxWireBytes budget. The budget is computed against the spec-defined
// TmpxMaxKidLen rather than the currently advertised kid — a JWKS rotation
// can change the kid length between seals, and a prefix that just fits today
// must still fit if the kid grows from 1 to 8 chars at the next refresh.
// When cfg.Priority is empty and the candidates don't all fit, returns an
// error — the spec forbids arbitrary truncation.
func selectTmpxEntries(cfg *tmpxConfig, ids []tmproto.IdentityToken) ([]tmproto.TmpxEntry, error) {
	type candidate struct {
		priority int
		entry    tmproto.TmpxEntry
	}
	candidates := make([]candidate, 0, len(ids))
	for _, id := range ids {
		typeID, ok := uidToTmpxTypeID[id.UIDType]
		if !ok {
			continue
		}
		p := indexOfUIDType(cfg.Priority, id.UIDType)
		if len(cfg.Priority) > 0 && p < 0 {
			continue
		}
		bin, err := stubBinaryToken(typeID, id.UserToken)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{priority: p, entry: tmproto.TmpxEntry{TypeID: typeID, Token: bin}})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(cfg.Priority) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].priority < candidates[j].priority
		})
	}

	entries := make([]tmproto.TmpxEntry, 0, len(candidates))
	usedBytes := 0
	for _, c := range candidates {
		need := 1 + len(c.entry.Token)
		nextWire := tmproto.TmpxWireSize(tmproto.TmpxMaxKidLen, usedBytes+need)
		if nextWire > tmproto.TmpxMaxWireBytes {
			if len(cfg.Priority) == 0 {
				return nil, fmt.Errorf("tmpx wire size %d exceeds %d-byte budget and no --tmpx-priority configured: spec forbids arbitrary truncation",
					nextWire, tmproto.TmpxMaxWireBytes)
			}
			break
		}
		entries = append(entries, c.entry)
		usedBytes += need
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("tmpx wire budget %d cannot fit even the highest-priority entry", tmproto.TmpxMaxWireBytes)
	}
	return entries, nil
}

// indexOfUIDType returns the position of uid in list, or -1 if absent.
func indexOfUIDType(list []tmproto.UIDType, uid tmproto.UIDType) int {
	for i, u := range list {
		if u == uid {
			return i
		}
	}
	return -1
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
			return nil, errors.New("--registry-url (or TMP_IDENTITY_REGISTRY_URL) is required for signature verification (default); pass --allow-unsigned to opt out")
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
