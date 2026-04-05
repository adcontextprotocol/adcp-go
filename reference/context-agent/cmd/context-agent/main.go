package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"time"

	contextagent "github.com/adcontextprotocol/adcp-go/reference/context-agent"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func main() {
	addr := flag.String("addr", ":8081", "Listen address")
	registryFile := flag.String("registry", "", "Path to registry snapshot JSON file")
	flag.Parse()

	// Load property registry.
	registry := contextagent.NewPropertyRegistry()
	if *registryFile != "" {
		if err := registry.LoadFromFile(*registryFile); err != nil {
			log.Fatalf("Failed to load registry: %v", err)
		}
		log.Printf("Loaded %d properties from registry", registry.Len())
	}

	// Build global property bitmap from registry using Roaring.
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
		Properties: targeting.PropertyList{
			Global: &contextagent.RoaringBitmap{Bitmap: tc.PropertyBitmap},
		},
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-display-0041", TopicTargets: true, EmitSegments: []string{"food", "lifestyle"}},
			{PackageID: "pkg-native-0078", TopicTargets: true, URLBlocklist: true, EmitSegments: []string{"technology"}},
		},
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

		result, err := engine.EvaluateContext(r.Context(), &req)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
				RequestID: req.RequestID,
				Code:      tmproto.ErrorCodeInternalError,
				Message:   err.Error(),
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
