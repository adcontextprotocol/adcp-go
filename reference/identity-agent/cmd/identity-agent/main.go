package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/prommetrics"
	"github.com/adcontextprotocol/adcp-go/targeting/valkeystore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/redis/go-redis/v9"
)

var version = "dev"

func main() {
	addr := flag.String("addr", "", "Listen address")
	redisAddr := flag.String("redis-addr", "", "Redis/Valkey address (e.g. localhost:6379). Falls back to in-memory store if empty or unreachable.")
	flag.Parse()

	// Resolve config: flags > env vars > defaults.
	listenAddr := resolveAddr(*addr)
	storeAddr := resolveRedisAddr(*redisAddr)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	metrics := prommetrics.New()
	store := initStore(storeAddr)
	resolved := seedConfigs(store)

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

func resolveRedisAddr(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("TMP_IDENTITY_REDIS_ADDR")
}

// initStore creates a ValkeyStore if redis-addr is provided and reachable,
// otherwise falls back to an in-memory MockStore.
func initStore(redisAddr string) targeting.Store {
	if redisAddr == "" {
		slog.Info("No redis address configured, using in-memory store")
		return targeting.NewMockStore()
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		PoolSize:     20,
		MinIdleConns: 5,
		MaxRetries:   2,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Warn("Cannot reach Redis, falling back to in-memory store", "addr", redisAddr, "error", err)
		return targeting.NewMockStore()
	}

	slog.Info("Connected to Redis/Valkey", "addr", redisAddr)
	return valkeystore.New(rdb)
}

// seedConfigs pushes reference identity and campaign configs into the Store
// and returns the resolved package indexes for identity evaluation.
func seedConfigs(store targeting.Store) *targeting.ResolvedPackages {
	ctx := context.Background()

	configs := []struct {
		pkgID string
		cfg   targeting.PackageIdentityConfig
	}{
		{"pkg-display-0041", targeting.PackageIdentityConfig{
			CampaignID:     "campaign-acme-q1",
			FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
			Audience:       true,
		}},
		{"pkg-display-0042", targeting.PackageIdentityConfig{
			CampaignID:     "campaign-acme-q1",
			FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 43200}},
		}},
		{"pkg-native-0078", targeting.PackageIdentityConfig{
			CampaignID: "campaign-nova-spring",
			FrequencyRules: []targeting.FrequencyRuleJSON{
				{MaxCount: 2, WindowSeconds: 43200},
				{MaxCount: 5, WindowSeconds: 604800},
			},
			Audience: true,
		}},
	}
	idConfigs := make(map[string]*targeting.PackageIdentityConfig, len(configs))
	for _, c := range configs {
		if err := targeting.SeedPackageIdentityConfig(ctx, store, c.pkgID, c.cfg); err != nil {
			slog.Error("seed package config failed", "package_id", c.pkgID, "error", err)
			os.Exit(1)
		}
		cfg := c.cfg
		idConfigs[c.pkgID] = &cfg
	}

	campaigns := []struct {
		campaignID string
		cfg        targeting.CampaignFreqConfig
	}{
		{"campaign-acme-q1", targeting.CampaignFreqConfig{
			FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 10, WindowSeconds: 604800}},
		}},
		{"campaign-nova-spring", targeting.CampaignFreqConfig{
			FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 15, WindowSeconds: 2592000}},
		}},
	}
	campConfigs := make(map[string]*targeting.CampaignFreqConfig, len(campaigns))
	for _, c := range campaigns {
		if err := targeting.SeedCampaignFreqConfig(ctx, store, c.campaignID, c.cfg); err != nil {
			slog.Error("seed campaign config failed", "campaign_id", c.campaignID, "error", err)
			os.Exit(1)
		}
		cfg := c.cfg
		campConfigs[c.campaignID] = &cfg
	}

	return &targeting.ResolvedPackages{
		IdentityConfigs: idConfigs,
		CampaignConfigs: campConfigs,
	}
}
