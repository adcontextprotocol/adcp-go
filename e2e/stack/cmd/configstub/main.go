// Command configstub serves the identity-config source the identity-agent
// polls at CONFIG_SOURCE_URL.
//
// Wire contract (see targeting/identityconfig/scope3):
//
//	POST /v1/identity-configs
//	Authorization: Bearer <token>
//	{ "after": "<RFC3339 nano>" }          (optional)
//
//	→ 200 { "lastUpdatedAt": ..., "targetingConfigs": [...], "removedTargetingConfigs": [...] }
//
// The stub returns its full package set on every poll, whether or not the
// caller sent `after`. Configs are upserts keyed on
// (sellerAgentUrl, packageId), so re-delivering the same set is a no-op —
// which is what makes the agent's refresh loop safe to run against a stub
// that has no change history.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/e2e/stack/internal/fixture"
)

type segmentRule struct {
	AllOf  []string `json:"allOf,omitempty"`
	AnyOf  []string `json:"anyOf,omitempty"`
	NoneOf []string `json:"noneOf,omitempty"`
}

type targetingConfig struct {
	SellerAgentURL string       `json:"sellerAgentUrl"`
	PackageID      string       `json:"packageId"`
	TargetSegments *segmentRule `json:"targetSegments,omitempty"`
}

type removedEntry struct {
	SellerAgentURL string `json:"sellerAgentUrl"`
	PackageID      string `json:"packageId"`
}

type responseBody struct {
	LastUpdatedAt           time.Time         `json:"lastUpdatedAt"`
	TargetingConfigs        []targetingConfig `json:"targetingConfigs"`
	RemovedTargetingConfigs []removedEntry    `json:"removedTargetingConfigs"`
}

func main() {
	port := flag.Int("port", fixture.ConfigStubPort, "listen port")
	flag.Parse()

	body := responseBody{
		LastUpdatedAt:           time.Now().UTC(),
		TargetingConfigs:        configs(),
		RemovedTargetingConfigs: []removedEntry{},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		log.Fatalf("configstub: marshal snapshot: %v", err)
	}

	var polls atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/identity-configs", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixture.ConfigSourceToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"config_polls": polls.Load()})
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("configstub: listening on %s (%d packages under %s)",
		addr, len(body.TargetingConfigs), fixture.SellerAgentURL)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("configstub: serve: %v", err)
	}
}

// configs returns one entry per identity package. Only the audience package
// carries a segment rule; the capped package is gated purely by the
// frequency-cap marker the seeder wrote, so the two exclusion mechanisms
// stay separately attributable in the assertions.
func configs() []targetingConfig {
	return []targetingConfig{
		{
			SellerAgentURL: fixture.SellerAgentURL,
			PackageID:      fixture.PackageIdentityOpen,
		},
		{
			SellerAgentURL: fixture.SellerAgentURL,
			PackageID:      fixture.PackageIdentityAudience,
			TargetSegments: &segmentRule{AnyOf: []string{fixture.AudienceSegment}},
		},
		{
			SellerAgentURL: fixture.SellerAgentURL,
			PackageID:      fixture.PackageIdentityCapped,
		},
	}
}
