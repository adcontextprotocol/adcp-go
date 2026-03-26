package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func main() {
	addr := flag.String("addr", ":8081", "Listen address")
	registryFile := flag.String("registry", "", "Path to registry snapshot JSON file")
	flag.Parse()

	// Initialize Valkey client (mock for reference implementation)
	valkey := NewMockValkeyClient()

	// Load registry
	registry := NewPropertyRegistry()
	if *registryFile != "" {
		if err := registry.LoadFromFile(*registryFile); err != nil {
			log.Fatalf("Failed to load registry: %v", err)
		}
		log.Printf("Loaded %d properties from registry", registry.Len())
	}

	// Build targeting config from registry
	targeting := NewTargetingConfig()
	for _, rid := range registry.AllRIDs() {
		targeting.AddProperties(rid)
	}

	// Seed sample topics for reference demo
	valkey.SAdd("topics:package:pkg-display-0041", "food.cooking", "food.recipes", "lifestyle.home")
	valkey.SAdd("topics:package:pkg-native-0078", "technology.gadgets", "technology.reviews")

	// Create modules
	urlModule := NewURLPatternModule(valkey)
	topicModule := NewTopicMatchModule(valkey)

	agent := NewAgent(AgentConfig{
		ProviderID:                "reference-context-agent",
		Registry:                  registry,
		Targeting:                 targeting,
		Valkey:                    valkey,
		Modules:                   []Module{urlModule, topicModule},
		SignatureSampleRate: 0, // Disabled for reference demo (no keys configured)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", func(w http.ResponseWriter, r *http.Request) {
		var req tmp.ContextMatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(tmp.ErrorResponse{
				Code:    tmp.ErrorCodeInvalidRequest,
				Message: err.Error(),
			})
			return
		}

		resp, err := agent.ContextMatch(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(tmp.ErrorResponse{
				RequestID: req.RequestID,
				Code:      tmp.ErrorCodeInternalError,
				Message:   err.Error(),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	log.Printf("Context Agent listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
