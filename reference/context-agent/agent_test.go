package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func setupTestAgent(t *testing.T) (*Agent, *MockValkeyClient) {
	t.Helper()

	valkey := NewMockValkeyClient()
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	// Register 5 properties
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for i := uint32(1); i <= 5; i++ {
		registry.Put(&PropertyRecord{
			RID:       i,
			Domain:    "example.com",
			PublicKey: pub,
		})
		targeting.AddProperties(i)
	}

	agent := NewAgent(AgentConfig{
		ProviderID:                "test-provider",
		Registry:                  registry,
		Targeting:                 targeting,
		Valkey:                    valkey,
		SignatureSampleRate: 0,
	})

	return agent, valkey
}

func TestBitmapPreFilter_Targeted(t *testing.T) {
	agent, _ := setupTestAgent(t)
	ctx := context.Background()

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-1",
		PropertyRID: 1,
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1"},
		},
	}

	resp, err := agent.ContextMatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer for targeted property, got %d", len(resp.Offers))
	}
}

func TestBitmapPreFilter_NotTargeted(t *testing.T) {
	agent, _ := setupTestAgent(t)
	ctx := context.Background()

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-2",
		PropertyRID: 999, // Not in targeting bitmap
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1"},
		},
	}

	resp, err := agent.ContextMatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers for untargeted property, got %d", len(resp.Offers))
	}
}

func TestPropertySuppression(t *testing.T) {
	agent, _ := setupTestAgent(t)
	ctx := context.Background()

	// Suppress property RID 2
	agent.suppressions.SuppressProperty(ctx, 2, time.Hour)

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-3",
		PropertyRID: 2,
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1"},
		},
	}

	resp, err := agent.ContextMatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers for suppressed property, got %d", len(resp.Offers))
	}
}

func TestPerPackageTargeting(t *testing.T) {
	agent, _ := setupTestAgent(t)
	ctx := context.Background()

	// pkg-scoped only targets property 3
	agent.targeting.AddPackageProperties("pkg-scoped", 3)

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-4",
		PropertyRID: 1, // In global bitmap but not in pkg-scoped
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-scoped"},
		},
	}

	resp, err := agent.ContextMatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers (property not in package bitmap), got %d", len(resp.Offers))
	}

	// Now request with property 3
	req.PropertyRID = 3
	resp, err = agent.ContextMatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer (property in package bitmap), got %d", len(resp.Offers))
	}
}

func TestModulePipeline_TopicMatch(t *testing.T) {
	valkey := NewMockValkeyClient()
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	registry.Put(&PropertyRecord{RID: 10, Domain: "food.example.com", PublicKey: pub})
	targeting.AddProperties(10)

	// Set up topic data in Valkey
	valkey.SAdd("topics:package:pkg-food", "food.cooking", "food.baking")
	valkey.SAdd("topics:artifact:article:pasta-recipe", "food.cooking", "food.italian")

	topicMod := NewTopicMatchModule(valkey)

	agent := NewAgent(AgentConfig{
		ProviderID:                "test-provider",
		Registry:                  registry,
		Targeting:                 targeting,
		Valkey:                    valkey,
		Modules:                   []Module{topicMod},
		SignatureSampleRate: 0,
	})

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-5",
		PropertyRID: 10,
		Artifacts:   []string{"article:pasta-recipe"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food"},
		},
	}

	resp, err := agent.ContextMatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer (topic match), got %d", len(resp.Offers))
	}
}

func TestModulePipeline_TopicMiss(t *testing.T) {
	valkey := NewMockValkeyClient()
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	registry.Put(&PropertyRecord{RID: 10, Domain: "tech.example.com", PublicKey: pub})
	targeting.AddProperties(10)

	valkey.SAdd("topics:package:pkg-food", "food.cooking", "food.baking")
	valkey.SAdd("topics:artifact:article:cpu-review", "technology.hardware", "technology.reviews")

	topicMod := NewTopicMatchModule(valkey)

	agent := NewAgent(AgentConfig{
		ProviderID:                "test-provider",
		Registry:                  registry,
		Targeting:                 targeting,
		Valkey:                    valkey,
		Modules:                   []Module{topicMod},
		SignatureSampleRate: 0,
	})

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-6",
		PropertyRID: 10,
		Artifacts:   []string{"article:cpu-review"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food"},
		},
	}

	resp, err := agent.ContextMatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers (topic mismatch), got %d", len(resp.Offers))
	}
}

func TestModulePipeline_URLBlocklist(t *testing.T) {
	valkey := NewMockValkeyClient()
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	registry.Put(&PropertyRecord{RID: 20, Domain: "news.example.com", PublicKey: pub})
	targeting.AddProperties(20)

	// Block a specific URL hash
	blockedHash := hashURL("article:controversial-post")
	valkey.SAdd("url:blocklist:pkg-family", blockedHash)

	urlMod := NewURLPatternModule(valkey)

	agent := NewAgent(AgentConfig{
		ProviderID:                "test-provider",
		Registry:                  registry,
		Targeting:                 targeting,
		Valkey:                    valkey,
		Modules:                   []Module{urlMod},
		SignatureSampleRate: 0,
	})

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-7",
		PropertyRID: 20,
		Artifacts:   []string{"article:controversial-post"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-family"},
		},
	}

	resp, err := agent.ContextMatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers (URL blocked), got %d", len(resp.Offers))
	}
}

func TestSignatureVerification(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	registry := NewPropertyRegistry()
	registry.Put(&PropertyRecord{RID: 100, Domain: "secure.example.com", PublicKey: pub})

	req := &tmp.ContextMatchRequest{
		RequestID:   "signed-1",
		PropertyRID: 100,
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID: "sidebar",
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1"},
		},
	}

	sig := SignRequest(req, priv)

	// Valid signature
	err := VerifyRequestSignature(req, sig, registry)
	if err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}

	// Tampered signature (flip a character in the base64 string)
	tampered := "AAAA" + sig[4:]
	err = VerifyRequestSignature(req, tampered, registry)
	if err == nil {
		t.Error("tampered signature accepted")
	}

	// Unknown property
	req.PropertyRID = 999
	err = VerifyRequestSignature(req, sig, registry)
	if err == nil {
		t.Error("unknown property accepted")
	}
}

func TestMultiplePackages_MixedResults(t *testing.T) {
	valkey := NewMockValkeyClient()
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	registry.Put(&PropertyRecord{RID: 30, Domain: "multi.example.com", PublicKey: pub})
	targeting.AddProperties(30)

	// pkg-food targets food topics, pkg-tech targets tech topics
	valkey.SAdd("topics:package:pkg-food", "food.cooking")
	valkey.SAdd("topics:package:pkg-tech", "technology.reviews")
	valkey.SAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	topicMod := NewTopicMatchModule(valkey)

	agent := NewAgent(AgentConfig{
		ProviderID:                "test-provider",
		Registry:                  registry,
		Targeting:                 targeting,
		Valkey:                    valkey,
		Modules:                   []Module{topicMod},
		SignatureSampleRate: 0,
	})

	req := &tmp.ContextMatchRequest{
		RequestID:   "test-multi",
		PropertyRID: 30,
		Artifacts:   []string{"article:pasta"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food"},
			{PackageID: "pkg-tech"},
		},
	}

	resp, err := agent.ContextMatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	matched := map[string]bool{}
	for _, o := range resp.Offers {
		matched[o.PackageID] = true
	}

	if !matched["pkg-food"] {
		t.Error("pkg-food should match (food article, food targeting)")
	}
	if matched["pkg-tech"] {
		t.Error("pkg-tech should NOT match (food article, tech targeting)")
	}
}

func TestRegistrySync(t *testing.T) {
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()
	targeting.AddProperties(1, 2, 3)

	events := []RegistryEvent{
		{Sequence: 1, Action: "register", Record: PropertyRecord{RID: 10, Domain: "new.example.com"}},
		{Sequence: 2, Action: "deactivate", Record: PropertyRecord{RID: 2}},
	}

	ApplyEvents(registry, targeting, events)

	if registry.Get(10) == nil {
		t.Error("property 10 should be registered")
	}
	if registry.Get(2) != nil {
		t.Error("property 2 should be deactivated")
	}
	if targeting.ContainsProperty(2) {
		t.Error("property 2 should be removed from targeting bitmap")
	}
	if registry.Sequence != 2 {
		t.Errorf("expected sequence 2, got %d", registry.Sequence)
	}
}

func TestEmptyResponse_RequestIDPreserved(t *testing.T) {
	agent, _ := setupTestAgent(t)
	ctx := context.Background()

	req := &tmp.ContextMatchRequest{
		RequestID:   "preserve-me",
		PropertyRID: 999, // Not targeted
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1"},
		},
	}

	resp, err := agent.ContextMatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "preserve-me" {
		t.Errorf("expected request_id 'preserve-me', got '%s'", resp.RequestID)
	}
}
