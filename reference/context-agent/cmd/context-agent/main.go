package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	contextagent "github.com/adcontextprotocol/adcp-go/reference/context-agent"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/prommetrics"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// referenceTaxonomy is the demo taxonomy the reference agent seeds with. A
// real deployment configures its accepted taxonomies via env / flag and
// matches the writers that populate its Valkey instance.
var referenceTaxonomy = topicstore.Taxonomy{Source: "reference", ID: 1}

var version = "dev"

func main() {
	addr := flag.String("addr", "", "Listen address")
	registryFile := flag.String("registry", "", "Path to registry snapshot JSON file")
	registryURL := flag.String("registry-url", "", "URL of the router's /registry/snapshot endpoint for signing-key discovery")
	allowUnsigned := flag.Bool("allow-unsigned", false, "Accept /tmp/context requests without a TMP signature. Default is deny — TMP signing is normative in the spec. Use only for migration windows or local dev.")
	ownEndpointURL := flag.String("own-endpoint-url", "", "This provider's registered endpoint URL (must match the router's provider registration). Required for signature verification (default).")
	flag.Parse()

	flagSet := setFlags()

	// Resolve config: flags > env vars > defaults.
	listenAddr := resolveAddr(*addr)
	regFile := resolveRegistry(*registryFile, flagSet["registry"])
	regURL := resolveString(*registryURL, flagSet["registry-url"], "TMP_CONTEXT_REGISTRY_URL")
	ownURL := resolveString(*ownEndpointURL, flagSet["own-endpoint-url"], "TMP_CONTEXT_ENDPOINT_URL")
	if !flagSet["allow-unsigned"] {
		if envValue, ok := os.LookupEnv("TMP_CONTEXT_ALLOW_UNSIGNED"); ok {
			*allowUnsigned = envValue == "1" || envValue == "true"
		}
	}
	requireSig := !*allowUnsigned

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	metrics := prommetrics.New()

	// Load property registry.
	registry := contextagent.NewPropertyRegistry()
	if regFile != "" {
		if err := registry.LoadFromFile(regFile); err != nil {
			slog.Error("Failed to load registry", "path", regFile, "error", err)
			os.Exit(1)
		}
		slog.Info("Loaded properties from registry", "count", registry.Len())
	}

	// Build global property bitmap from registry.
	tc := contextagent.NewTargetingConfig()
	for _, rid := range registry.AllRIDs() {
		tc.AddProperties(rid)
	}

	// Seed sample data in mock store.
	store := targeting.NewMockStore()
	seedCtx := context.Background()
	writer, wErr := topicstore.NewWriter(store)
	if wErr != nil {
		slog.Error("topicstore writer init failed", "error", wErr)
		os.Exit(1)
	}
	if err := writer.SetPackageTopics(seedCtx, referenceTaxonomy, "pkg-display-0041", []string{"food.cooking", "food.recipes", "lifestyle.home"}); err != nil {
		slog.Error("seed package topics failed", "error", err)
		os.Exit(1)
	}
	if err := writer.SetPackageTopics(seedCtx, referenceTaxonomy, "pkg-native-0078", []string{"technology.gadgets", "technology.reviews"}); err != nil {
		slog.Error("seed package topics failed", "error", err)
		os.Exit(1)
	}

	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID: "reference-context-agent",
		Store:      store,
		Metrics:    metrics,
		Properties: targeting.PropertyList{
			Global: tc.PropertyBitmap,
		},
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-display-0041", TopicTargets: true, EmitSegments: []string{"food", "lifestyle"}},
			{PackageID: "pkg-native-0078", TopicTargets: true, URLBlocklist: true, EmitSegments: []string{"technology"}},
		},
		AcceptedTaxonomies: []topicstore.Taxonomy{referenceTaxonomy},
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
		slog.Warn("/tmp/context accepts unsigned requests — TMP signing should be required in production")
	}

	mux := http.NewServeMux()
	contextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
				Code:    tmproto.ErrorCodeInvalidRequest,
				Message: "failed to read request body",
			})
			return
		}
		var req tmproto.ContextMatchRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
				Code:    tmproto.ErrorCodeInvalidRequest,
				Message: "request body is not valid JSON",
			})
			return
		}

		result, err := engine.EvaluateContext(r.Context(), &req)
		if err != nil {
			slog.Error("EvaluateContext failed", "request_id", req.RequestID, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
				RequestID: req.RequestID,
				Code:      tmproto.ErrorCodeInternalError,
				Message:   "internal error",
			})
			return
		}

		resp := &tmproto.ContextMatchResponse{
			RequestID: result.RequestID,
			Offers:    result.Offers,
			Signals:   result.Signals,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		slog.Debug("context match", "request_id", req.RequestID, "offers", len(result.Offers), "latency_ms", time.Since(start).Milliseconds())
	})

	if keystore != nil {
		mux.Handle("POST /tmp/context", tmproto.VerifyContextMatchHandler(contextHandler, tmproto.VerifyOptions{
			KeyStore:         keystore,
			OwnEndpointURL:   ownURL,
			RequireSignature: requireSig,
		}))
	} else {
		mux.Handle("POST /tmp/context", contextHandler)
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
	slog.Info("Context Agent starting", "addr", listenAddr, "version", version)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("listen error", "error", err)
		os.Exit(1)
	}
}

func resolveAddr(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("TMP_CONTEXT_ADDR"); env != "" {
		return env
	}
	return ":8081"
}

func resolveRegistry(flagVal string, flagSet bool) string {
	if flagSet {
		return flagVal
	}
	if v := os.Getenv("TMP_CONTEXT_REGISTRY"); v != "" {
		return v
	}
	return flagVal
}

func resolveString(flagVal string, flagSet bool, envName string) string {
	if flagSet {
		return flagVal
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return flagVal
}

func setFlags() map[string]bool {
	out := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { out[f.Name] = true })
	return out
}

func buildKeyStore(runCtx context.Context, registryURL string, requireSignature bool) (tmproto.KeyStore, error) {
	if registryURL == "" {
		if requireSignature {
			return nil, errors.New("--registry-url (or TMP_CONTEXT_REGISTRY_URL) is required for signature verification (default); pass --allow-unsigned to opt out")
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
