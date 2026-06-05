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
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/contextstorage"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/targeting/urlliststore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

var benchTaxonomy = topicstore.Taxonomy{Source: "bench", ID: 1}

// BenchmarkBitmapCheck tests the string-set bitmap Contains() with 50K properties, targeting 1K.
func BenchmarkBitmapCheck(b *testing.B) {
	bm := NewSetBitmap()
	for i := range 1000 {
		bm.Add(fmt.Sprintf("prop-%d", i*50))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rid := fmt.Sprintf("prop-%d", i%50000)
		_ = bm.Contains(rid)
	}
}

// BenchmarkSignatureVerify tests Ed25519 verify of a TMP context-match
// signature using the spec envelope (X-AdCP-Signature).
func BenchmarkSignatureVerify(b *testing.B) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := tmproto.NewSigner("bench-kid", priv)
	ks := tmproto.NewStaticKeyStore([]tmproto.SigningKey{tmproto.PublicSigningKey("bench-kid", pub)})
	endpoint := "https://provider.example.com"
	now := time.Now()
	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-sig",
		PropertyRID:  "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:benchmark-test"}},
		PackageIDs:   []string{"pkg-1"},
	}
	sig := signer.SignContextMatch(req, endpoint, tmproto.EpochAt(now))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tmproto.VerifyContextMatch(req, endpoint, sig, signer.KeyID, ks, now)
	}
}

// BenchmarkFullPipeline tests complete context evaluation with bitmap + topic match.
func BenchmarkFullPipeline(b *testing.B) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-food", TopicTargets: true, URLBlocklist: true}).
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-tech", TopicTargets: true}).
		WithPackageTopics(benchTaxonomy, "pkg-food", []string{"food.cooking", "food.baking", "food.italian"}).
		WithArtifactTopics(benchTaxonomy, "article:pasta-recipe", []string{"food.cooking", "food.italian"})

	bm := NewSetBitmap()
	for i := 1; i <= 1000; i++ {
		bm.Add(fmt.Sprintf("prop-%d", i))
	}

	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID:         "bench-provider",
		Storage:            storage,
		Properties:         targeting.PropertyList{Global: bm},
		AcceptedTaxonomies: []topicstore.Taxonomy{benchTaxonomy},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-pipeline",
		PropertyRID:  "prop-500",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta-recipe"}},
		PackageIDs:   []string{"pkg-food", "pkg-tech"},
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = engine.Evaluate(ctx, req)
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
	for i := range 50000 {
		s.Records = append(s.Records, &PropertyRecord{
			RID:       fmt.Sprintf("prop-%d", i),
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

// BenchmarkURLLookup tests URL block-set membership through the
// urlliststore reader (mock backing store).
func BenchmarkURLLookup(b *testing.B) {
	store := urlliststore.NewMockStore()
	svc, _ := urlliststore.NewService(store)
	ctx := context.Background()
	for i := range 10000 {
		_ = svc.AddToBlocklist(ctx, "pkg-1", tmproto.HashURL(fmt.Sprintf("article:content-%d", i)))
	}
	r := urlliststore.NewReader(store)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		urlHash := tmproto.HashURL(fmt.Sprintf("article:content-%d", i%20000))
		_, _ = r.IsBlocked(ctx, "pkg-1", urlHash)
	}
}

// BenchmarkSignatureSign tests Ed25519 signing (router-side cost) using the
// TMP envelope (X-AdCP-Signature).
func BenchmarkSignatureSign(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := tmproto.NewSigner("bench-kid", priv)
	endpoint := "https://provider.example.com"
	epoch := tmproto.CurrentEpoch()
	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-sign",
		PropertyRID:  "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:benchmark-test"}},
		PackageIDs:   []string{"pkg-1"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = signer.SignContextMatch(req, endpoint, epoch)
	}
}

// BenchmarkHMACSign tests HMAC-SHA256 signing as an alternative to Ed25519.
func BenchmarkHMACSign(b *testing.B) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	req := &tmproto.ContextMatchRequest{
		RequestID:    "bench-hmac",
		PropertyRID:  "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:benchmark-test"}},
		PackageIDs:   []string{"pkg-1"},
	}
	payload := tmproto.BuildContextMatchSigningInput(req, "https://provider.example.com", tmproto.CurrentEpoch())

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
		PropertyRID:  "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:benchmark-test"}},
		PackageIDs:   []string{"pkg-1"},
	}
	payload := tmproto.BuildContextMatchSigningInput(req, "https://provider.example.com", tmproto.CurrentEpoch())

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
	signer, _ := tmproto.NewSigner("bench-kid", priv)
	endpoint := "https://provider.example.com"
	epoch := tmproto.CurrentEpoch()

	for i := range 1000 {
		key := fmt.Sprintf("placement-%d:pkghash-abc", i)
		req := &tmproto.ContextMatchRequest{
			RequestID:   fmt.Sprintf("req-%d", i),
			PropertyRID: fmt.Sprintf("prop-%d", i),
			PlacementID: fmt.Sprintf("placement-%d", i),
			PackageIDs:  []string{"pkg-1"},
		}
		cache[key] = signer.SignContextMatch(req, endpoint, epoch)
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
