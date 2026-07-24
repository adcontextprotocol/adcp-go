// configserver is a static mock of the Scope3 identity-config source the
// identity-agent polls at CONFIG_SOURCE_URL. It generates a synthetic set of
// packages — some with an audience-membership rule, some without — sized by
// env vars so the benchmark can shape the config surface without a code
// change.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

type wireSegmentRule struct {
	AllOf  []string `json:"allOf,omitempty"`
	AnyOf  []string `json:"anyOf,omitempty"`
	NoneOf []string `json:"noneOf,omitempty"`
}

type wireConfig struct {
	SellerAgentURL string           `json:"sellerAgentUrl"`
	PackageID      string           `json:"packageId"`
	TargetSegments *wireSegmentRule `json:"targetSegments,omitempty"`
}

type responseBody struct {
	LastUpdatedAt           time.Time    `json:"lastUpdatedAt"`
	TargetingConfigs        []wireConfig `json:"targetingConfigs"`
	RemovedTargetingConfigs []struct {
		SellerAgentURL string `json:"sellerAgentUrl"`
		PackageID      string `json:"packageId"`
	} `json:"removedTargetingConfigs"`
}

func main() {
	port := envInt("PORT", 9001)
	sellerAgentURL := envStr("SELLER_AGENT_URL", "https://seller.perf.local/agent")
	totalPackages := envInt("TOTAL_PACKAGES", 200)
	audiencePackages := envInt("AUDIENCE_PACKAGES", 100)
	audiencesPerPackage := envInt("AUDIENCES_PER_PACKAGE", 3)
	totalAudiences := envInt("TOTAL_AUDIENCES", 500)
	authToken := envStr("AUTH_TOKEN", "perf-bench-token")

	if audiencePackages > totalPackages {
		log.Fatalf("AUDIENCE_PACKAGES (%d) > TOTAL_PACKAGES (%d)", audiencePackages, totalPackages)
	}

	body := buildSnapshot(sellerAgentURL, totalPackages, audiencePackages, audiencesPerPackage, totalAudiences)
	payload, err := json.Marshal(body)
	if err != nil {
		log.Fatalf("marshal snapshot: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/identity-configs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+authToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := fmt.Sprintf(":%d", port)
	log.Printf("configserver listening on %s (packages=%d, audience_packages=%d)", addr, totalPackages, audiencePackages)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func buildSnapshot(sellerURL string, totalPackages, audiencePackages, audiencesPerPackage, totalAudiences int) responseBody {
	configs := make([]wireConfig, 0, totalPackages)
	for i := 0; i < totalPackages; i++ {
		pkg := wireConfig{
			SellerAgentURL: sellerURL,
			PackageID:      fmt.Sprintf("pkg-%05d", i),
		}
		if i < audiencePackages {
			any := make([]string, 0, audiencesPerPackage)
			for j := 0; j < audiencesPerPackage; j++ {
				id := (i*audiencesPerPackage + j) % totalAudiences
				any = append(any, fmt.Sprintf("aud-%05d", id))
			}
			pkg.TargetSegments = &wireSegmentRule{AnyOf: any}
		}
		configs = append(configs, pkg)
	}
	return responseBody{
		LastUpdatedAt:    time.Now().UTC(),
		TargetingConfigs: configs,
	}
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("%s=%q is not an integer: %v", name, v, err)
	}
	return n
}
