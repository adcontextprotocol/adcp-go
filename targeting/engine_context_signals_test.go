package targeting

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingStore wraps a ContextStore and counts SetMembers calls keyed on a
// substring of the looked-up key. Used to assert which Valkey paths the
// engine traverses for a given request shape.
type countingStore struct {
	ContextStore
	prefix string
	hits   atomic.Int32
}

func (c *countingStore) SetMembers(ctx context.Context, key string) ([]string, error) {
	if c.prefix == "" || containsSubstring(key, c.prefix) {
		c.hits.Add(1)
	}
	return c.ContextStore.SetMembers(ctx, key)
}

func containsSubstring(s, sub string) bool {
	return len(sub) <= len(s) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// resolvedFromConfigs builds a minimal ResolvedPackages whose ContextConfigs
// the engine reads to decide TopicTargets, and whose TopicIndex maps the
// taxonomy-namespaced topics to their packages. Tests use it to drive the
// resolved path with a hand-built fixture.
func resolvedFromConfigs(tax topicstore.Taxonomy, pkgs map[string][]string) *ResolvedPackages {
	resolved := &ResolvedPackages{
		ContextConfigs: make(map[string]*PackageContextConfig, len(pkgs)),
		TopicIndex:     make(map[string][]string),
	}
	for pkgID, topics := range pkgs {
		resolved.ContextConfigs[pkgID] = &PackageContextConfig{PackageID: pkgID, TopicTargets: true}
		for _, t := range topics {
			ns := topicstore.NamespaceTopic(tax, t)
			resolved.TopicIndex[ns] = append(resolved.TopicIndex[ns], pkgID)
		}
	}
	return resolved
}

func TestContextResolved_PublisherTopicsUnion(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	store := NewMockStore()
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{tax},
	})
	resolved := resolvedFromConfigs(tax, map[string][]string{
		"pkg-food": {"632"},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "1",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "iab",
			TaxonomyID:     7,
			Topics:         []string{"632"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "publisher topic should match pkg-food without any Valkey artifact-topic data")
}

func TestContextResolved_PublisherTopicsRejectedForUnacceptedTaxonomy(t *testing.T) {
	accepted := topicstore.Taxonomy{Source: "iab", ID: 7}
	store := NewMockStore()
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{accepted},
	})
	resolved := resolvedFromConfigs(accepted, map[string][]string{
		"pkg-food": {"632"},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-food"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "rogue",
			TaxonomyID:     99,
			Topics:         []string{"632"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "publisher topics from an unaccepted taxonomy must be ignored")
}

func TestContextResolved_ShortCircuitsValkeyWhenPublisherCoversAllPackages(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	base := NewMockStore()
	store := &countingStore{ContextStore: base, prefix: "topics:artifact:"}

	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{tax},
	})
	resolved := resolvedFromConfigs(tax, map[string][]string{
		"pkg-food": {"632"},
		"pkg-tech": {"640"},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "1",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:x"}},
		PackageIDs:   []string{"pkg-food", "pkg-tech"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "iab",
			TaxonomyID:     7,
			Topics:         []string{"632", "640"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 2, "both packages should activate from publisher topics alone")
	assert.Equal(t, int32(0), store.hits.Load(), "no Valkey artifact-topic lookups when publisher topics cover every TopicTargets package")
}

func TestContextResolved_FallsBackToValkeyWhenPublisherTopicsIncomplete(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	base := NewMockStore()
	writer, err := topicstore.NewWriter(base)
	require.NoError(t, err)
	require.NoError(t, writer.SetArtifactTopics(context.Background(), tax, "article:x", []string{"640"}))

	store := &countingStore{ContextStore: base, prefix: "topics:artifact:"}

	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{tax},
	})
	resolved := resolvedFromConfigs(tax, map[string][]string{
		"pkg-food": {"632"}, // publisher covers this one
		"pkg-tech": {"640"}, // needs Valkey to discover
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "1",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:x"}},
		PackageIDs:   []string{"pkg-food", "pkg-tech"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "iab",
			TaxonomyID:     7,
			Topics:         []string{"632"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 2, "both packages activate after union of publisher + Valkey topics")
	assert.Greater(t, store.hits.Load(), int32(0), "Valkey must be consulted when publisher topics don't cover every package")
}

func TestContextResolved_NoArtifactsButPublisherTopicsActivate(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	store := NewMockStore()
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{tax},
	})
	resolved := resolvedFromConfigs(tax, map[string][]string{
		"pkg-food": {"632"},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-food"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "iab",
			TaxonomyID:     7,
			Topics:         []string{"632"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "ephemeral content (no artifact refs) must still match via publisher topics")
}

func TestContextResolved_MultiTaxonomyIsolation(t *testing.T) {
	iab := topicstore.Taxonomy{Source: "iab", ID: 7}
	custom := topicstore.Taxonomy{Source: "custom", ID: 1}
	base := NewMockStore()

	// pkg-food targets topic "632" under IAB; pkg-tech targets "632" under
	// custom. The two should not cross-match.
	resolved := &ResolvedPackages{
		ContextConfigs: map[string]*PackageContextConfig{
			"pkg-food": {PackageID: "pkg-food", TopicTargets: true},
			"pkg-tech": {PackageID: "pkg-tech", TopicTargets: true},
		},
		TopicIndex: map[string][]string{
			topicstore.NamespaceTopic(iab, "632"):    {"pkg-food"},
			topicstore.NamespaceTopic(custom, "632"): {"pkg-tech"},
		},
	}

	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              base,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{iab, custom},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-food", "pkg-tech"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "iab",
			TaxonomyID:     7,
			Topics:         []string{"632"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	require.Len(t, resp.Offers, 1)
	assert.Equal(t, "pkg-food", resp.Offers[0].PackageID, "only the IAB-side package should activate")
}

func TestContextResolved_NoTopicTargetsBypassesUnion(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	base := NewMockStore()
	store := &countingStore{ContextStore: base, prefix: "topics:artifact:"}

	resolved := &ResolvedPackages{
		ContextConfigs: map[string]*PackageContextConfig{
			"pkg-nontopic": {PackageID: "pkg-nontopic", TopicTargets: false},
		},
	}
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{tax},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "1",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:x"}},
		PackageIDs:   []string{"pkg-nontopic"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "iab",
			TaxonomyID:     7,
			Topics:         []string{"anything"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
	assert.Equal(t, int32(0), store.hits.Load(), "non-TopicTargets package must not trigger artifact-topic Valkey lookups")
}
