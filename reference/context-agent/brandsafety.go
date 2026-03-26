package main

import (
	"context"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// BrandSafetyModule rejects packages if content has any category in the
// package's blocked list. Categories stored as Valkey sets.
type BrandSafetyModule struct {
	valkey ValkeyClient
}

func NewBrandSafetyModule(valkey ValkeyClient) *BrandSafetyModule {
	return &BrandSafetyModule{valkey: valkey}
}

func (m *BrandSafetyModule) Name() string { return "brand_safety" }

func (m *BrandSafetyModule) Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))

	// Collect content safety categories from all artifacts
	contentCategories := make(map[string]struct{})
	for _, artifact := range req.Artifacts {
		cats, _ := m.valkey.SMembers(ctx, "safety:artifact:"+artifact)
		for _, cat := range cats {
			contentCategories[cat] = struct{}{}
		}
	}

	for _, pkg := range packages {
		activate := true
		if len(contentCategories) > 0 {
			blocked, _ := m.valkey.SMembers(ctx, "safety:blocked:"+pkg.PackageID)
			for _, b := range blocked {
				if _, found := contentCategories[b]; found {
					activate = false
					break
				}
			}
		}
		score := float32(0)
		if activate {
			score = 1.0
		}
		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     score,
		})
	}
	return results
}
