package main

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestEmbeddingMatch_SimilarVectors(t *testing.T) {
	v := NewMockValkeyClient()
	artEmb, _ := json.Marshal([]float64{1, 0, 0, 0})
	pkgEmb, _ := json.Marshal([]float64{0.9, 0.1, 0, 0})
	_ = v.Set(context.Background(), "embedding:artifact:article:test", string(artEmb), 0)
	_ = v.Set(context.Background(), "embedding:package:pkg-1", string(pkgEmb), 0)

	mod := NewEmbeddingMatchModule(v)
	mod.Threshold = 0.9
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:test"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("similar vectors should activate")
	}
}

func TestEmbeddingMatch_DissimilarVectors(t *testing.T) {
	v := NewMockValkeyClient()
	artEmb, _ := json.Marshal([]float64{1, 0, 0, 0})
	pkgEmb, _ := json.Marshal([]float64{0, 1, 0, 0})
	_ = v.Set(context.Background(), "embedding:artifact:article:test", string(artEmb), 0)
	_ = v.Set(context.Background(), "embedding:package:pkg-1", string(pkgEmb), 0)

	mod := NewEmbeddingMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:test"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("orthogonal vectors should not activate")
	}
}

func TestEmbeddingMatch_MissingEmbedding(t *testing.T) {
	v := NewMockValkeyClient()
	mod := NewEmbeddingMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{
		Artifacts: []string{"article:test"},
	}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("missing embedding should not activate")
	}
}

func TestEmbeddingMatch_NoArtifacts(t *testing.T) {
	v := NewMockValkeyClient()
	mod := NewEmbeddingMatchModule(v)
	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("no artifacts should pass through")
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		a, b []float64
		want float64
	}{
		{[]float64{1, 0}, []float64{1, 0}, 1.0},
		{[]float64{1, 0}, []float64{0, 1}, 0.0},
		{[]float64{1, 1}, []float64{1, 1}, 1.0},
		{[]float64{}, []float64{}, 0.0},
		{[]float64{1}, []float64{1, 2}, 0.0}, // different lengths
	}
	for _, tt := range tests {
		got := cosineSimilarity(tt.a, tt.b)
		if math.Abs(got-tt.want) > 0.001 {
			t.Errorf("cosineSimilarity(%v, %v) = %f, want %f", tt.a, tt.b, got, tt.want)
		}
	}
}
