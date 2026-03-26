package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
	"github.com/redis/go-redis/v9"
)

func main() {
	addr := flag.String("addr", ":8082", "Listen address")
	redisAddr := flag.String("redis", "localhost:6379", "Valkey/Redis address")
	flag.Parse()

	rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})

	// Try to connect to Valkey; fall back to in-memory if unavailable
	var store Store
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("Valkey unavailable (%v), using in-memory store", err)
		store = NewInMemoryStore()
	} else {
		log.Printf("Connected to Valkey at %s", *redisAddr)
		store = NewRedisStore(rdb)
	}

	agent := NewIdentityAgent(store,
		[]PackageConfig{
			{PackageID: "pkg-display-0041", CampaignID: "campaign-acme-q1", FrequencyRules: []FrequencyRule{{MaxCount: 5, Window: 24 * time.Hour}}, TargetSegments: []string{"cooking_enthusiast", "home_improvement"}},
			{PackageID: "pkg-display-0042", CampaignID: "campaign-acme-q1", FrequencyRules: []FrequencyRule{{MaxCount: 3, Window: 12 * time.Hour}}},
			{PackageID: "pkg-native-0078", CampaignID: "campaign-nova-spring", FrequencyRules: []FrequencyRule{{MaxCount: 2, Window: 12 * time.Hour}, {MaxCount: 5, Window: 7 * 24 * time.Hour}}, TargetSegments: []string{"organic_food"}},
		},
		[]CampaignConfig{
			{CampaignID: "campaign-acme-q1", FrequencyRules: []FrequencyRule{{MaxCount: 10, Window: 7 * 24 * time.Hour}}},
			{CampaignID: "campaign-nova-spring", FrequencyRules: []FrequencyRule{{MaxCount: 15, Window: 30 * 24 * time.Hour}}},
		},
	)

	mux := http.NewServeMux()

	mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, r *http.Request) {
		var req tmp.IdentityMatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmp.ErrorResponse{Code: tmp.ErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		resp, err := agent.IdentityMatch(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmp.ErrorResponse{RequestID: req.RequestID, Code: tmp.ErrorCodeInternalError, Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("POST /tmp/expose", func(w http.ResponseWriter, r *http.Request) {
		var req tmp.ExposeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmp.ErrorResponse{Code: tmp.ErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		resp, err := agent.Expose(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmp.ErrorResponse{Code: tmp.ErrorCodeInternalError, Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "degraded", "store": "in-memory"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "store": "valkey"})
	})

	log.Printf("Identity Agent listening on %s, Valkey at %s", *addr, *redisAddr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
