package targeting

import (
	"context"
	"strings"
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
	if c.prefix == "" || strings.Contains(key, c.prefix) {
		c.hits.Add(1)
	}
	return c.ContextStore.SetMembers(ctx, key)
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

// TestContextResolved_ShortCircuitsWhenOnlyPackageIsNonTopicTargets
// verifies the short-circuit fires when the request asks about a package
// that has no TopicTargets rule — `publisherCoversTopicTargets` returns
// true vacuously and no artifact-topic lookups happen.
func TestContextResolved_ShortCircuitsWhenOnlyPackageIsNonTopicTargets(t *testing.T) {
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
	assert.Equal(t, int32(0), store.hits.Load(), "no TopicTargets package in the request → no artifact-topic Valkey lookups")
}

// TestContextResolved_EmptyAcceptedTaxonomies_FailsClosed pins the
// fail-closed semantic the ContextEngineConfig doc promises. A package
// configured with TopicTargets must NOT activate if the deployment
// declares no accepted taxonomies — even when there's no topic input on
// the request that would normally trigger the vacuous-pass path.
func TestContextResolved_EmptyAcceptedTaxonomies_FailsClosed(t *testing.T) {
	store := NewMockStore()
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID: "test",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("1")},
		// AcceptedTaxonomies intentionally empty.
	})
	resolved := &ResolvedPackages{
		ContextConfigs: map[string]*PackageContextConfig{
			"pkg-food": {PackageID: "pkg-food", TopicTargets: true},
		},
	}

	req := &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-food"},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "TopicTargets package must fail-closed when no taxonomies are configured")
}

// TestContextResolved_RogueTaxonomyPlusRealArtifact verifies the subtle
// case where the publisher mis-declared a taxonomy on ContextSignals AND
// also sent legitimate artifact refs whose stored topics match. The rogue
// ContextSignals.Topics is silently dropped (not in accepted set), but
// the artifact lookup still runs under the accepted taxonomy and the
// package activates from artifact-side data alone.
func TestContextResolved_RogueTaxonomyPlusRealArtifact(t *testing.T) {
	accepted := topicstore.Taxonomy{Source: "iab", ID: 7}
	base := NewMockStore()
	w, err := topicstore.NewWriter(base)
	require.NoError(t, err)
	require.NoError(t, w.SetArtifactTopics(context.Background(), accepted, "article:pasta", []string{"632"}))

	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              base,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{accepted},
	})
	resolved := resolvedFromConfigs(accepted, map[string][]string{
		"pkg-food": {"632"},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "1",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "rogue",
			TaxonomyID:     99,
			Topics:         []string{"anything"},
		},
	}

	resp, err := engine.EvaluateContextResolved(context.Background(), resolved, req)
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "rogue ContextSignals must not poison legitimate artifact-side match")
}

// TestContext_NonResolvedPath_PublisherTopicsHonored covers item 3 from
// review: the non-resolved EvaluateContext path must consume
// ContextSignals.Topics when the declared taxonomy is accepted, in
// addition to the artifact-side SetIntersect path. Without artifact refs
// in the request, only publisher topics drive the match.
func TestContext_NonResolvedPath_PublisherTopicsHonored(t *testing.T) {
	tax := topicstore.Taxonomy{Source: "iab", ID: 7}
	store := NewMockStore()
	ctx := context.Background()
	w, err := topicstore.NewWriter(store)
	require.NoError(t, err)
	require.NoError(t, w.SetPackageTopics(ctx, tax, "pkg-food", []string{"632", "640"}))

	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		Packages:           []PackageConfig{{PackageID: "pkg-food", TopicTargets: true}},
		AcceptedTaxonomies: []topicstore.Taxonomy{tax},
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

	resp, err := engine.EvaluateContext(ctx, req)
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "non-resolved path must honor ContextSignals.Topics for accepted taxonomies")
}

// TestContext_NonResolvedPath_RogueOnlySource_FailsClosed pins the
// fail-closed shape on the non-resolved path when the publisher's
// ContextSignals.Topics is the only topic source AND its taxonomy is
// not accepted. The package's TopicTargets contract requires a real
// match; the rogue-declared topics are dropped and no Valkey data is
// reachable, so the package must not activate.
func TestContext_NonResolvedPath_RogueOnlySource_FailsClosed(t *testing.T) {
	accepted := topicstore.Taxonomy{Source: "iab", ID: 7}
	store := NewMockStore()
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		Packages:           []PackageConfig{{PackageID: "pkg-food", TopicTargets: true}},
		AcceptedTaxonomies: []topicstore.Taxonomy{accepted},
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-food"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "rogue",
			TaxonomyID:     99,
			Topics:         []string{"anything"},
		},
	}

	resp, err := engine.EvaluateContext(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "non-resolved path must fail-closed when rogue ContextSignals is the only topic source")
}

// TestContext_NonResolvedPath_EmptyAcceptedTaxonomies_FailsClosed mirrors
// the resolved-path fail-closed test for the non-resolved engine path.
func TestContext_NonResolvedPath_EmptyAcceptedTaxonomies_FailsClosed(t *testing.T) {
	store := NewMockStore()
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID: "test",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("1")},
		Packages:   []PackageConfig{{PackageID: "pkg-food", TopicTargets: true}},
		// AcceptedTaxonomies intentionally empty.
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "1",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:x"}},
		PackageIDs:   []string{"pkg-food"},
	}

	resp, err := engine.EvaluateContext(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "TopicTargets package must fail-closed when no taxonomies are configured on the non-resolved path")
}

// TestContext_NewContextEngine_DefensiveCopiesAcceptedTaxonomies makes
// sure the slice the caller passes in cannot be mutated post-construction
// to silently change engine behavior.
func TestContext_NewContextEngine_DefensiveCopiesAcceptedTaxonomies(t *testing.T) {
	caller := []topicstore.Taxonomy{{Source: "iab", ID: 7}}
	engine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "test",
		Store:              NewMockStore(),
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: caller,
	})
	caller[0] = topicstore.Taxonomy{Source: "rogue", ID: 99}

	assert.True(t, engine.acceptsTaxonomy(topicstore.Taxonomy{Source: "iab", ID: 7}),
		"engine must keep its own copy; post-construction mutation must not reach in")
	assert.False(t, engine.acceptsTaxonomy(topicstore.Taxonomy{Source: "rogue", ID: 99}))
}
