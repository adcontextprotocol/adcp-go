package main

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/RoaringBitmap/roaring"
	"github.com/adcontextprotocol/adcp-go/tmp"
)

// BenchmarkBitmapCheck tests Roaring bitmap Contains() with 50K properties, targeting 1K.
func BenchmarkBitmapCheck(b *testing.B) {
	bm := roaring.New()
	// Add 1K targeted properties out of 50K universe
	for i := uint32(0); i < 1000; i++ {
		bm.Add(i * 50) // Spread across the 50K range
	}
	bm.RunOptimize()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rid := uint32(i % 50000)
		_ = bm.Contains(rid)
	}
}

// BenchmarkSignatureVerify tests Ed25519 verify.
func BenchmarkSignatureVerify(b *testing.B) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	registry := NewPropertyRegistry()
	registry.Put(&PropertyRecord{RID: 1, Domain: "bench.example.com", PublicKey: pub})

	req := &tmp.ContextMatchRequest{
		RequestID:    "bench-sig",
		PropertyRID:  1,
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}
	sig := SignRequest(req, priv)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = VerifyRequestSignature(req, sig, registry)
	}
}

// BenchmarkFullPipeline tests complete ContextMatch call with bitmap + modules.
func BenchmarkFullPipeline(b *testing.B) {
	valkey := NewMockValkeyClient()
	valkey.latency = 0 // Remove simulated latency for benchmark purity

	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for i := uint32(1); i <= 1000; i++ {
		registry.Put(&PropertyRecord{RID: i, Domain: fmt.Sprintf("prop-%d.example.com", i), PublicKey: pub})
		targeting.AddProperties(i)
	}
	targeting.PropertyBitmap.RunOptimize()

	// Set up topic data
	valkey.SAdd("topics:package:pkg-food", "food.cooking", "food.baking", "food.italian")
	valkey.SAdd("topics:artifact:article:pasta-recipe", "food.cooking", "food.italian")

	topicMod := NewTopicMatchModule(valkey)
	urlMod := NewURLPatternModule(valkey)

	agent := NewAgent(AgentConfig{
		ProviderID:                "bench-provider",
		Registry:                  registry,
		Targeting:                 targeting,
		Valkey:                    valkey,
		Modules:                   []Module{urlMod, topicMod},
		SignatureSampleRate: 0,
	})

	req := &tmp.ContextMatchRequest{
		RequestID:   "bench-pipeline",
		PropertyRID: 500,
		Artifacts:   []string{"article:pasta-recipe"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
			{PackageID: "pkg-tech", MediaBuyID: "mb-2"},
		},
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = agent.ContextMatch(ctx, req)
	}
}

// BenchmarkRegistryLoad tests loading a 50K property registry.
func BenchmarkRegistryLoad(b *testing.B) {
	// Build a JSON snapshot with 50K records
	type snapshot struct {
		Sequence uint64            `json:"sequence"`
		Records  []*PropertyRecord `json:"records"`
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	s := snapshot{Sequence: 1}
	for i := uint32(0); i < 50000; i++ {
		s.Records = append(s.Records, &PropertyRecord{
			RID:       i,
			Domain:    fmt.Sprintf("prop-%d.example.com", i),
			PublicKey: pub,
		})
	}
	data, _ := json.Marshal(s)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg := NewPropertyRegistry()
		_ = reg.LoadFromJSON(data)
	}
}

// BenchmarkValkeyLookup tests URL pattern check using mock Valkey.
func BenchmarkValkeyLookup(b *testing.B) {
	valkey := NewMockValkeyClient()
	valkey.latency = 0 // Remove simulated latency for raw performance

	// Add 10K URLs to blocklist
	for i := 0; i < 10000; i++ {
		valkey.SAdd("url:blocklist:pkg-1", hashURL(fmt.Sprintf("article:content-%d", i)))
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		urlHash := hashURL(fmt.Sprintf("article:content-%d", i%20000))
		_, _ = valkey.SIsMember(ctx, "url:blocklist:pkg-1", urlHash)
	}
}

// BenchmarkSignatureSign tests Ed25519 signing (router-side cost).
func BenchmarkSignatureSign(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	req := &tmp.ContextMatchRequest{
		RequestID:    "bench-sign",
		PropertyRID:  1,
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SignRequest(req, priv)
	}
}

// BenchmarkHMACSign tests HMAC-SHA256 signing as an alternative to Ed25519.
func BenchmarkHMACSign(b *testing.B) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	req := &tmp.ContextMatchRequest{
		RequestID:    "bench-hmac",
		PropertyRID:  1,
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}
	payload := canonicalizeRequest(req, currentEpoch())

	mac := hmac.New(sha256.New, key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mac.Reset()
		mac.Write(payload)
		_ = mac.Sum(nil)
	}
}

// BenchmarkHMACVerify tests HMAC-SHA256 verification.
func BenchmarkHMACVerify(b *testing.B) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	req := &tmp.ContextMatchRequest{
		RequestID:    "bench-hmac-v",
		PropertyRID:  1,
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}
	payload := canonicalizeRequest(req, currentEpoch())

	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	sig := mac.Sum(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mac2 := hmac.New(sha256.New, key)
		mac2.Write(payload)
		expected := mac2.Sum(nil)
		_ = hmac.Equal(sig, expected)
	}
}

// BenchmarkCachedSignature tests the cost when signatures are pre-computed
// per (placement_id, package_set_hash) — the router caches the signature
// because available_packages is stable per placement.
func BenchmarkCachedSignature(b *testing.B) {
	cache := make(map[string]string, 1000)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Pre-fill cache with 1000 placement signatures
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("placement-%d:pkghash-abc", i)
		req := &tmp.ContextMatchRequest{
			RequestID:    fmt.Sprintf("req-%d", i),
			PropertyRID:  uint64(i),
			PlacementID:  fmt.Sprintf("placement-%d", i),
			AvailablePkgs: []tmp.AvailablePackage{
				{PackageID: "pkg-1", MediaBuyID: "mb-1"},
			},
		}
		cache[key] = SignRequest(req, priv)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("placement-%d:pkghash-abc", i%1000)
		_ = cache[key] // cache hit: map lookup only
	}
}

// BenchmarkOpenRTBEquivalent simulates the equivalent OpenRTB operation:
// parse a full BidRequest JSON, do string-based URL matching, and check
// targeting rules with string comparison.
func BenchmarkOpenRTBEquivalent(b *testing.B) {
	// Simulate an OpenRTB BidRequest (simplified)
	type BidRequest struct {
		ID   string `json:"id"`
		Site struct {
			Domain string `json:"domain"`
			Page   string `json:"page"`
		} `json:"site"`
		Imp []struct {
			ID     string `json:"id"`
			Banner struct {
				W int `json:"w"`
				H int `json:"h"`
			} `json:"banner"`
		} `json:"imp"`
	}

	reqJSON := []byte(`{
		"id": "bench-ortb-1",
		"site": {
			"domain": "www.oakwoodpublishing.example.com",
			"page": "https://www.oakwoodpublishing.example.com/recipes/pasta-carbonara"
		},
		"imp": [
			{"id": "imp-1", "banner": {"w": 300, "h": 250}},
			{"id": "imp-2", "banner": {"w": 728, "h": 90}}
		]
	}`)

	// Targeting: list of allowed domains (string comparison)
	allowedDomains := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		allowedDomains[fmt.Sprintf("www.publisher-%d.example.com", i)] = true
	}
	allowedDomains["www.oakwoodpublishing.example.com"] = true

	// URL blocklist (string matching)
	blockedURLPrefixes := make([]string, 100)
	for i := range blockedURLPrefixes {
		blockedURLPrefixes[i] = fmt.Sprintf("https://www.blocked-%d.example.com", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var req BidRequest
		_ = json.Unmarshal(reqJSON, &req)

		// Domain check (string map lookup)
		_ = allowedDomains[req.Site.Domain]

		// URL blocklist check (string prefix matching)
		for _, prefix := range blockedURLPrefixes {
			if strings.HasPrefix(req.Site.Page, prefix) {
				break
			}
		}
	}
}
