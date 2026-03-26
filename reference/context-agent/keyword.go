package main

import (
	"context"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// KeywordMatchModule checks if content keywords overlap with package target
// keywords. Keywords are stored as Valkey sets.
type KeywordMatchModule struct {
	valkey   ValkeyClient
	MinMatch int // Minimum keyword overlap count (default 1)
}

func NewKeywordMatchModule(valkey ValkeyClient) *KeywordMatchModule {
	return &KeywordMatchModule{valkey: valkey, MinMatch: 1}
}

func (m *KeywordMatchModule) Name() string { return "keyword_match" }

func (m *KeywordMatchModule) Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))
	for _, pkg := range packages {
		activate := false
		var bestScore float32

		for _, artifact := range req.Artifacts {
			intersection, _ := m.valkey.SInter(ctx, "keywords:package:"+pkg.PackageID, "keywords:artifact:"+artifact)
			if len(intersection) >= m.MinMatch {
				activate = true
				score := float32(len(intersection)) / 10.0
				if score > 1.0 {
					score = 1.0
				}
				if score > bestScore {
					bestScore = score
				}
			}
		}

		if len(req.Artifacts) == 0 {
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
