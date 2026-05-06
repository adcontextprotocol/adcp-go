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
	flag.Parse()

	listenAddr := resolveAddr(*addr)
	storeAddr := resolveValkeyAddr(*valkeyAddr)

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

	mux := http.NewServeMux()

	mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		slog.Debug("identity match", "request_id", req.RequestID, "packages", len(req.PackageIDs), "latency_ms", time.Since(start).Milliseconds())
	})

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
