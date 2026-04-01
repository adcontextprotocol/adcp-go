package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"time"

	contextagent "github.com/adcontextprotocol/adcp-go/reference/context-agent"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func main() {
	addr := flag.String("addr", ":8081", "Listen address")
	registryFile := flag.String("registry", "", "Path to registry snapshot JSON file")
	flag.Parse()

	// Initialize Valkey client (mock for reference implementation)
	valkey := contextagent.NewMockValkeyClient()

	// Load registry
	registry := contextagent.NewPropertyRegistry()
	if *registryFile != "" {
		if err := registry.LoadFromFile(*registryFile); err != nil {
			log.Fatalf("Failed to load registry: %v", err)
		}
		log.Printf("Loaded %d properties from registry", registry.Len())
	}

	// Build targeting config from registry
	targeting := contextagent.NewTargetingConfig()
	for _, rid := range registry.AllRIDs() {
		targeting.AddProperties(rid)
	}

	// Seed sample topics for reference demo
	valkey.SAdd("topics:package:pkg-display-0041", "food.cooking", "food.recipes", "lifestyle.home")
	valkey.SAdd("topics:package:pkg-native-0078", "technology.gadgets", "technology.reviews")

	// Create modules
	urlModule := contextagent.NewURLPatternModule(valkey)
	topicModule := contextagent.NewTopicMatchModule(valkey)

	agent := contextagent.NewAgent(contextagent.AgentConfig{
		ProviderID:          "reference-context-agent",
		Registry:            registry,
		Targeting:           targeting,
		Valkey:              valkey,
		Modules:             []contextagent.Module{urlModule, topicModule},
		SignatureSampleRate: 0, // Disabled for reference demo (no keys configured)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", func(w http.ResponseWriter, r *http.Request) {
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

		resp, err := agent.ContextMatch(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
				RequestID: req.RequestID,
				Code:      tmproto.ErrorCodeInternalError,
				Message:   err.Error(),
			})
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
	log.Printf("Context Agent listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
