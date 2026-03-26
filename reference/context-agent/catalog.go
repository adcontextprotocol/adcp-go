package main

import (
	"context"
	"encoding/json"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// CatalogItem represents an item in a buyer's product catalog.
type CatalogItem struct {
	ItemID     string   `json:"item_id"`
	Categories []string `json:"categories"`
}

// CatalogMatchModule matches content topics to product catalog categories.
// If any content topic intersects with any catalog item's categories, match.
type CatalogMatchModule struct {
	valkey ValkeyClient
}

func NewCatalogMatchModule(valkey ValkeyClient) *CatalogMatchModule {
	return &CatalogMatchModule{valkey: valkey}
}

func (m *CatalogMatchModule) Name() string { return "catalog_match" }

func (m *CatalogMatchModule) Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))

	// Collect content topics from all artifacts
	contentTopics := make(map[string]struct{})
	for _, artifact := range req.Artifacts {
		topics, _ := m.valkey.SMembers(ctx, "topics:artifact:"+artifact)
		for _, t := range topics {
			contentTopics[t] = struct{}{}
		}
	}

	for _, pkg := range packages {
		activate := false
		var bestScore float32

		if len(contentTopics) > 0 {
			raw, _ := m.valkey.Get(ctx, "catalog:"+pkg.PackageID)
			if raw != "" {
				var items []CatalogItem
				if json.Unmarshal([]byte(raw), &items) == nil {
					matchCount := 0
					for _, item := range items {
						for _, cat := range item.Categories {
							if _, found := contentTopics[cat]; found {
								matchCount++
								break
							}
						}
					}
					if matchCount > 0 {
						activate = true
						bestScore = float32(matchCount) / float32(len(items))
						if bestScore > 1.0 {
							bestScore = 1.0
						}
					}
				}
			}
		} else if len(req.Artifacts) == 0 {
			activate = true
			bestScore = 0.5
		}

		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     bestScore,
		})
	}
	return results
}
