// Command context-agent (reference) is a minimal-config TMP context-match
// service intended for local development, integration tests, and the
// AGENTS.md walkthrough — NOT for production. The production agent
// lives at cmd/context-agent and reads its configuration from the
// environment with operational safeguards (LRU caches, suppression
// refresh, structured shutdown, image-signing CI). This reference
// stays optimized for "small Dockerfile + one ./reference run" loops.
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

	contextagentref "github.com/adcontextprotocol/adcp-go/reference/context-agent"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/contextstorage"
	"github.com/adcontextprotocol/adcp-go/targeting/prommetrics"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// referenceTaxonomy is the demo taxonomy the reference agent seeds with.
// A real deployment configures its accepted taxonomies via env and
// matches the writers that populate its storage.
var referenceTaxonomy = topicstore.Taxonomy{Source: "reference", ID: 1}

var version = "dev"

func main() {
	addr := flag.String("addr", "", "Listen address")
	registryFile := flag.String("registry", "", "Path to registry snapshot JSON file")
	registryURL := flag.String("registry-url", "", "URL of the router's /registry/snapshot endpoint for signing-key discovery")
	allowUnsigned := flag.Bool("allow-unsigned", false, "Accept /tmp/context requests without a TMP signature.")
	ownEndpointURL := flag.String("own-endpoint-url", "", "This provider's registered endpoint URL.")
	flag.Parse()

	flagSet := setFlags()

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

	registry := contextagentref.NewPropertyRegistry()
	if regFile != "" {
		if err := registry.LoadFromFile(regFile); err != nil {
			slog.Error("Failed to load registry", "path", regFile, "error", err)
			os.Exit(1)
		}
		slog.Info("Loaded properties from registry", "count", registry.Len())
	}

	tc := contextagentref.NewTargetingConfig()
	for _, rid := range registry.AllRIDs() {
		tc.AddProperties(rid)
	}

	// Seed the in-memory storage with two demo packages and a matching
	// artifact-topic fixture so a curl against /tmp/context shows
	// non-empty offers without standing up Valkey.
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{
			PackageID:    "pkg-display-0041",
			TopicTargets: true,
			EmitSegments: []string{"food", "lifestyle"},
		}).
		WithPackage(&targeting.PackageContextConfig{
			PackageID:    "pkg-native-0078",
			TopicTargets: true,
			EmitSegments: []string{"technology"},
		}).
		WithPackageTopics(referenceTaxonomy, "pkg-display-0041", []string{"food.cooking", "food.recipes", "lifestyle.home"}).
		WithPackageTopics(referenceTaxonomy, "pkg-native-0078", []string{"technology.gadgets", "technology.reviews"})

	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID: "reference-context-agent",
		Storage:    storage,
		Metrics:    metrics,
		Properties: targeting.PropertyList{
			Global: tc.PropertyBitmap,
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
		slog.Error("--own-endpoint-url is required when signature verification is enabled")
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

		result, err := engine.Evaluate(r.Context(), &req)
		if err != nil {
			slog.Error("Evaluate failed", "request_id", req.RequestID, "error", err)
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
	slog.Info("Context Agent (reference) starting", "addr", listenAddr, "version", version)
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
			return nil, errors.New("--registry-url is required for signature verification; pass --allow-unsigned to opt out")
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
