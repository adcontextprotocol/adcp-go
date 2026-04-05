package contextagent

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

	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// BenchmarkBitmapCheck tests Roaring bitmap Contains() with 50K properties, targeting 1K.
func BenchmarkBitmapCheck(b *testing.B) {
	bm := roaring64.New()
	for i := range uint64(1000) {
		bm.Add(i * 50)
	}
	bm.RunOptimize()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rid := uint64(i % 50000)
		_ = bm.Contains(rid)
	}
}

// BenchmarkSignatureVerify tests Ed25519 verify.
func BenchmarkSignatureVerify(b *testing.B) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-sig",
		PropertyRID:  1,
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}
	sig := tmproto.SignRequest(req, priv)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tmproto.VerifyRequestSignature(req, sig, pub)
	}
}

// BenchmarkFullPipeline tests complete context evaluation with bitmap + topic match.
func BenchmarkFullPipeline(b *testing.B) {
	store := targeting.NewMockStore()
	store.SetAdd("topics:package:pkg-food", "food.cooking", "food.baking", "food.italian")
	store.SetAdd("topics:artifact:article:pasta-recipe", "food.cooking", "food.italian")

	bm := roaring64.New()
	for i := uint64(1); i <= 1000; i++ {
		bm.Add(i)
	}
	bm.RunOptimize()

	engine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "bench-provider",
		Store:      store,
		Properties: targeting.PropertyList{
			Global: &RoaringBitmap{Bitmap: bm},
		},
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-food", TopicTargets: true, URLBlocklist: true},
			{PackageID: "pkg-tech", TopicTargets: true},
		},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:   "bench-pipeline",
		PropertyRID: 500,
		Artifacts:   []string{"article:pasta-recipe"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
			{PackageID: "pkg-tech", MediaBuyID: "mb-2"},
		},
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = engine.EvaluateContext(ctx, req)
	}
}

// BenchmarkRegistryLoad tests loading a 50K property registry.
func BenchmarkRegistryLoad(b *testing.B) {
	type snapshot struct {
		Sequence uint64            `json:"sequence"`
		Records  []*PropertyRecord `json:"records"`
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	s := snapshot{Sequence: 1}
	for i := range uint64(50000) {
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

// BenchmarkValkeyLookup tests URL pattern check using mock store.
func BenchmarkValkeyLookup(b *testing.B) {
	store := targeting.NewMockStore()
	for i := range 10000 {
		store.SetAdd("url:blocklist:pkg-1", targeting.HashURL(fmt.Sprintf("article:content-%d", i)))
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		urlHash := targeting.HashURL(fmt.Sprintf("article:content-%d", i%20000))
		_, _ = store.SetIsMember(ctx, "url:blocklist:pkg-1", urlHash)
	}
}

// BenchmarkSignatureSign tests Ed25519 signing (router-side cost).
func BenchmarkSignatureSign(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-sign",
		PropertyRID:  1,
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tmproto.SignRequest(req, priv)
	}
}

// BenchmarkHMACSign tests HMAC-SHA256 signing as an alternative to Ed25519.
func BenchmarkHMACSign(b *testing.B) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-hmac",
		PropertyRID:  1,
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}
	payload := tmproto.CanonicalizeForSigning(req, tmproto.CurrentEpoch())

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

	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-hmac-v",
		PropertyRID:  1,
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:benchmark-test"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}
	payload := tmproto.CanonicalizeForSigning(req, tmproto.CurrentEpoch())

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

// BenchmarkCachedSignature tests the cost when signatures are pre-computed.
func BenchmarkCachedSignature(b *testing.B) {
	cache := make(map[string]string, 1000)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	for i := range 1000 {
		key := fmt.Sprintf("placement-%d:pkghash-abc", i)
		req := &tmproto.ContextMatchRequest{
			RequestID:   fmt.Sprintf("req-%d", i),
			PropertyRID: uint64(i),
			PlacementID: fmt.Sprintf("placement-%d", i),
			AvailablePkgs: []tmproto.AvailablePackage{
				{PackageID: "pkg-1", MediaBuyID: "mb-1"},
			},
		}
		cache[key] = tmproto.SignRequest(req, priv)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("placement-%d:pkghash-abc", i%1000)
		_ = cache[key]
	}
}

// BenchmarkOpenRTBEquivalent simulates the equivalent OpenRTB operation.
func BenchmarkOpenRTBEquivalent(b *testing.B) {
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

	allowedDomains := make(map[string]bool, 1000)
	for i := range 1000 {
		allowedDomains[fmt.Sprintf("www.publisher-%d.example.com", i)] = true
	}
	allowedDomains["www.oakwoodpublishing.example.com"] = true

	blockedURLPrefixes := make([]string, 100)
	for i := range blockedURLPrefixes {
		blockedURLPrefixes[i] = fmt.Sprintf("https://www.blocked-%d.example.com", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var req BidRequest
		_ = json.Unmarshal(reqJSON, &req)
		_ = allowedDomains[req.Site.Domain]
		for _, prefix := range blockedURLPrefixes {
			if strings.HasPrefix(req.Site.Page, prefix) {
				break
			}
		}
	}
}
