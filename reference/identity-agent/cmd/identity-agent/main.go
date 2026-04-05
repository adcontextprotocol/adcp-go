package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func main() {
	addr := flag.String("addr", ":8082", "Listen address")
	flag.Parse()

	// Use mock store for reference implementation.
	store := targeting.NewMockStore()

	// Seed identity config in Store (data-driven).
	store.SetPackageIdentityConfig("pkg-display-0041", targeting.PackageIdentityConfig{
		CampaignID:     "campaign-acme-q1",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
		TargetSegments: []string{"cooking_enthusiast", "home_improvement"},
	})
	store.SetPackageIdentityConfig("pkg-display-0042", targeting.PackageIdentityConfig{
		CampaignID:     "campaign-acme-q1",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 43200}},
	})
	store.SetPackageIdentityConfig("pkg-native-0078", targeting.PackageIdentityConfig{
		CampaignID: "campaign-nova-spring",
		FrequencyRules: []targeting.FrequencyRuleJSON{
			{MaxCount: 2, WindowSeconds: 43200},
			{MaxCount: 5, WindowSeconds: 604800},
		},
		TargetSegments: []string{"organic_food"},
	})
	store.SetCampaignFreqConfig("campaign-acme-q1", targeting.CampaignFreqConfig{
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 10, WindowSeconds: 604800}},
	})
	store.SetCampaignFreqConfig("campaign-nova-spring", targeting.CampaignFreqConfig{
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 15, WindowSeconds: 2592000}},
	})

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
		result, err := engine.EvaluateIdentity(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{RequestID: req.RequestID, Code: tmproto.ErrorCodeInternalError, Message: err.Error()})
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
		resp, err := engine.RecordExposure(r.Context(), &req)
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
	log.Printf("Identity Agent listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
