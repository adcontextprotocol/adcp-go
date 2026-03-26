package main

import (
	"context"
	"encoding/json"
	"math"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// EmbeddingMatchModule computes cosine similarity between content embeddings
// and package target embeddings. Embeddings stored in Valkey as JSON float64 arrays.
type EmbeddingMatchModule struct {
	valkey    ValkeyClient
	Threshold float64 // Minimum cosine similarity (default 0.7)
}

func NewEmbeddingMatchModule(valkey ValkeyClient) *EmbeddingMatchModule {
	return &EmbeddingMatchModule{valkey: valkey, Threshold: 0.7}
}

func (m *EmbeddingMatchModule) Name() string { return "embedding_match" }

func (m *EmbeddingMatchModule) Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))

	// Load content embeddings from first artifact
	var contentEmb []float64
	if len(req.Artifacts) > 0 {
		raw, _ := m.valkey.Get(ctx, "embedding:artifact:"+req.Artifacts[0])
		if raw != "" {
			_ = json.Unmarshal([]byte(raw), &contentEmb)
		}
	}

	for _, pkg := range packages {
		activate := false
		var score float32

		if len(contentEmb) > 0 {
			raw, _ := m.valkey.Get(ctx, "embedding:package:"+pkg.PackageID)
			if raw != "" {
				var pkgEmb []float64
				if json.Unmarshal([]byte(raw), &pkgEmb) == nil {
					sim := cosineSimilarity(contentEmb, pkgEmb)
					if sim >= m.Threshold {
						activate = true
						score = float32(sim)
					}
				}
			}
		} else if len(req.Artifacts) == 0 {
			// No artifacts = pass through
			activate = true
			score = 0.5
		}

		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     score,
		})
	}
	return results
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
