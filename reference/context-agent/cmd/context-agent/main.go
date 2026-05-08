package main

import (
	"context"
	"encoding/json"
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
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

var version = "dev"

func main() {
	addr := flag.String("addr", "", "Listen address")
	registryFile := flag.String("registry", "", "Path to registry snapshot JSON file")
	registryURL := flag.String("registry-url", "", "URL of the router's /registry/snapshot endpoint for signing-key discovery")
	requireSig := flag.Bool("require-signature", false, "Reject /tmp/context requests that arrive without a TMP signature")
	ownEndpointURL := flag.String("own-endpoint-url", "", "This provider's registered endpoint URL (must match the router's provider registration). Required when --require-signature is set.")
	flag.Parse()

	// Resolve config: flags > env vars > defaults.
	listenAddr := resolveAddr(*addr)
	regFile := resolveRegistry(*registryFile)
	regURL := resolveString(*registryURL, "TMP_CONTEXT_REGISTRY_URL")
	ownURL := resolveString(*ownEndpointURL, "TMP_CONTEXT_ENDPOINT_URL")
	if envFlag := os.Getenv("TMP_CONTEXT_REQUIRE_SIGNATURE"); envFlag == "1" || envFlag == "true" {
		*requireSig = true
	}

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
	store.SetAdd("topics:package:pkg-display-0041", "food.cooking", "food.recipes", "lifestyle.home")
	store.SetAdd("topics:package:pkg-native-0078", "technology.gadgets", "technology.reviews")

	// Create targeting engine.
	engine := targeting.NewEngine(targeting.EngineConfig{
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
	})

	keystore, ksErr := buildKeyStore(regURL, *requireSig)
	if ksErr != nil {
		slog.Error("keystore init failed", "error", ksErr)
		os.Exit(1)
	}
	if *requireSig && ownURL == "" {
		slog.Error("--own-endpoint-url is required when --require-signature is set")
		os.Exit(1)
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
			RequireSignature: *requireSig,
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

func resolveRegistry(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("TMP_CONTEXT_REGISTRY")
}

func resolveString(flagVal, envName string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envName)
}

func buildKeyStore(registryURL string, requireSignature bool) (tmproto.KeyStore, error) {
	if registryURL == "" {
		if requireSignature {
			return nil, fmt.Errorf("--registry-url (or TMP_CONTEXT_REGISTRY_URL) is required when --require-signature is set")
		}
		return nil, nil
	}
	ks, err := tmproto.NewRemoteKeyStore(tmproto.RemoteKeyStoreOptions{URL: registryURL})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ks.Start(ctx); err != nil {
		return nil, fmt.Errorf("initial registry fetch from %s: %w", registryURL, err)
	}
	go func() {
		_ = ks.Start(context.Background())
	}()
	return ks, nil
}
