package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"

	identityagent "github.com/adcontextprotocol/adcp-go/reference/identity-agent"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/redis/go-redis/v9"
)

func main() {
	addr := flag.String("addr", ":8082", "Listen address")
	redisAddr := flag.String("redis", "localhost:6379", "Valkey/Redis address")
	flag.Parse()

	rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})

	agent := identityagent.NewIdentityAgent(rdb,
		[]identityagent.PackageConfig{
			{PackageID: "pkg-display-0041", CampaignID: "campaign-acme-q1", FrequencyRules: []identityagent.FrequencyRule{{MaxCount: 5, Window: 24 * time.Hour}}, TargetSegments: []string{"cooking_enthusiast", "home_improvement"}},
			{PackageID: "pkg-display-0042", CampaignID: "campaign-acme-q1", FrequencyRules: []identityagent.FrequencyRule{{MaxCount: 3, Window: 12 * time.Hour}}},
			{PackageID: "pkg-native-0078", CampaignID: "campaign-nova-spring", FrequencyRules: []identityagent.FrequencyRule{{MaxCount: 2, Window: 12 * time.Hour}, {MaxCount: 5, Window: 7 * 24 * time.Hour}}, TargetSegments: []string{"organic_food"}},
		},
		[]identityagent.CampaignConfig{
			{CampaignID: "campaign-acme-q1", FrequencyRules: []identityagent.FrequencyRule{{MaxCount: 10, Window: 7 * 24 * time.Hour}}},
			{CampaignID: "campaign-nova-spring", FrequencyRules: []identityagent.FrequencyRule{{MaxCount: 15, Window: 30 * 24 * time.Hour}}},
		},
	)

	mux := http.NewServeMux()

	mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, r *http.Request) {
		var req tmproto.IdentityMatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		resp, err := agent.IdentityMatch(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{RequestID: req.RequestID, Code: tmproto.ErrorCodeInternalError, Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("POST /tmp/expose", func(w http.ResponseWriter, r *http.Request) {
		var req tmproto.ExposeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		resp, err := agent.Expose(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInternalError, Message: err.Error()})
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
	log.Printf("Identity Agent listening on %s, Valkey at %s", *addr, *redisAddr)
	log.Fatal(srv.ListenAndServe())
}
