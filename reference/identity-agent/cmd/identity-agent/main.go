package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/valkeystore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/redis/go-redis/v9"
)

func main() {
	addr := flag.String("addr", ":8082", "Listen address")
	redisAddr := flag.String("redis-addr", "", "Redis/Valkey address (e.g. localhost:6379). Falls back to in-memory store if empty or unreachable.")
	flag.Parse()

	store := initStore(*redisAddr)
	seedConfigs(store)

	engine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "reference-identity-agent",
		Store:      store,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-display-0041"},
			{PackageID: "pkg-display-0042"},
			{PackageID: "pkg-native-0078"},
		},
	})

	mux := http.NewServeMux()

	mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, r *http.Request) {
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
		if req.UserToken == "" && len(req.Identities) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: "user_token or identities required"})
			return
		}
		result, err := engine.EvaluateIdentity(r.Context(), &req)
		if err != nil {
			log.Printf("EvaluateIdentity error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{RequestID: req.RequestID, Code: tmproto.ErrorCodeInternalError, Message: "internal error"})
			return
		}
		resp := &tmproto.IdentityMatchResponse{
			RequestID:   result.RequestID,
			Eligibility: result.Eligibility,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("POST /tmp/expose", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: "failed to read request body"})
			return
		}
		var req tmproto.ExposeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: "request body is not valid JSON"})
			return
		}
		if req.PackageID == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: "package_id is required"})
			return
		}
		if req.UserToken == "" && len(req.Identities) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: "user_token or identities required"})
			return
		}
		resp, err := engine.RecordExposure(r.Context(), &req)
		if err != nil {
			log.Printf("RecordExposure error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInternalError, Message: "internal error"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	log.Printf("Identity Agent listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

// initStore creates a ValkeyStore if redis-addr is provided and reachable,
// otherwise falls back to an in-memory MockStore.
func initStore(redisAddr string) targeting.Store {
	if redisAddr == "" {
		log.Println("No --redis-addr provided, using in-memory store")
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
		log.Printf("Cannot reach Redis at %s (%v), falling back to in-memory store", redisAddr, err)
		return targeting.NewMockStore()
	}

	log.Printf("Connected to Redis/Valkey at %s", redisAddr)
	return valkeystore.New(rdb)
}

// seedConfigs pushes reference identity and campaign configs into the Store.
func seedConfigs(store targeting.Store) {
	ctx := context.Background()

	configs := []struct {
		pkgID string
		cfg   targeting.PackageIdentityConfig
	}{
		{"pkg-display-0041", targeting.PackageIdentityConfig{
			CampaignID:     "campaign-acme-q1",
			FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
			TargetSegments: []string{"cooking_enthusiast", "home_improvement"},
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
			TargetSegments: []string{"organic_food"},
		}},
	}
	for _, c := range configs {
		if err := targeting.SeedPackageIdentityConfig(ctx, store, c.pkgID, c.cfg); err != nil {
			log.Fatalf("seed package config %s: %v", c.pkgID, err)
		}
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
	for _, c := range campaigns {
		if err := targeting.SeedCampaignFreqConfig(ctx, store, c.campaignID, c.cfg); err != nil {
			log.Fatalf("seed campaign config %s: %v", c.campaignID, err)
		}
	}
}
