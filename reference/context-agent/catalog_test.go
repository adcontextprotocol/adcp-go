package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestCatalogMatch_TopicIntersection(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")
	catalog, _ := json.Marshal([]CatalogItem{
		{ItemID: "olive-oil-1", Categories: []string{"food.cooking", "food.ingredients"}},
		{ItemID: "pan-1", Categories: []string{"kitchen.equipment"}},
	})
	_ = v.Set(context.Background(), "catalog:pkg-1", string(catalog), 0)

	mod := NewCatalogMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:pasta"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("should activate — food.cooking overlaps with catalog")
	}
	if results[0].Score <= 0 {
		t.Error("score should be positive")
	}
}

func TestCatalogMatch_NoIntersection(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("topics:artifact:article:tech", "technology.ai", "technology.ml")
	catalog, _ := json.Marshal([]CatalogItem{
		{ItemID: "oil-1", Categories: []string{"food.cooking"}},
	})
	_ = v.Set(context.Background(), "catalog:pkg-1", string(catalog), 0)

	mod := NewCatalogMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:tech"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("should not activate — no topic/catalog overlap")
	}
}

func TestCatalogMatch_NoCatalog(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("topics:artifact:article:test", "food.cooking")

	mod := NewCatalogMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:test"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("should not activate — no catalog defined")
	}
}

func TestCatalogMatch_NoArtifacts(t *testing.T) {
	v := NewMockValkeyClient()
	mod := NewCatalogMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("no artifacts should pass through")
	}
}

func TestCatalogMatch_MultipleItems(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("topics:artifact:article:cooking", "food.cooking", "food.baking")
	catalog, _ := json.Marshal([]CatalogItem{
		{ItemID: "flour-1", Categories: []string{"food.baking"}},
		{ItemID: "oil-1", Categories: []string{"food.cooking"}},
		{ItemID: "laptop-1", Categories: []string{"technology.computing"}},
	})
	_ = v.Set(context.Background(), "catalog:pkg-1", string(catalog), 0)

	mod := NewCatalogMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:cooking"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("should activate — 2 of 3 catalog items match")
	}
	// Score should be ~0.67 (2/3 items matched)
	if results[0].Score < 0.6 || results[0].Score > 0.7 {
		t.Errorf("expected score ~0.67, got %f", results[0].Score)
	}
}
