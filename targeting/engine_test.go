package targeting_test

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/contextstorage"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProviderID = "test-provider"
	testSeller     = "https://seller.example.com/agent"
)

var testTaxonomy = topicstore.Taxonomy{Source: "test", ID: 1}

func newEngine(t *testing.T, storage targeting.ContextStorage, opts ...func(*targeting.ContextEngineConfig)) *targeting.ContextEngine {
	t.Helper()
	cfg := targeting.ContextEngineConfig{
		ProviderID:         testProviderID,
		SellerAgentURL:     testSeller,
		Properties:         targeting.PropertyList{Global: targeting.NewMapBitmap("1", "2", "3", "10", "20", "30")},
		Storage:            storage,
		AcceptedTaxonomies: []topicstore.Taxonomy{testTaxonomy},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return targeting.NewContextEngine(cfg)
}

func TestContext_GlobalPropertyMiss(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-1"})
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "999",
		PackageIDs:  []string{"pkg-1"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestContext_PropertySuppressed(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-1"}).
		WithSuppressedProperty(testProviderID, "10")
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: "10", PackageIDs: []string{"pkg-1"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "suppressed property must short-circuit before any package activates")
}

func TestContext_GeoSuppressed(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-1"}).
		WithSuppressedGeo(testProviderID, "RU")
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: "10", PackageIDs: []string{"pkg-1"},
		Geo: map[string]any{"country": "RU"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestContext_ActivePackagesFromStorage(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-a"}).
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-b"}).
		WithActivePackages(testSeller, "pub-1", "US", []string{"pkg-a", "pkg-b"})

	engine := newEngine(t, storage)
	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "10",
		PropertyID:  "pub-1",
		Geo:         map[string]any{"country": "US"},
		// No PackageIDs — engine resolves from storage.
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 2)
}

func TestContext_TopicMatchViaArtifact(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-food", TopicTargets: true}).
		WithPackageTopics(testTaxonomy, "pkg-food", []string{"food.cooking"}).
		WithArtifactTopics(testTaxonomy, "article:pasta", []string{"food.cooking", "food.italian"})

	engine := newEngine(t, storage)
	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "10",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}

func TestContext_TopicMatchViaContextSignals(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-food", TopicTargets: true}).
		WithPackageTopics(testTaxonomy, "pkg-food", []string{"food.cooking"})

	engine := newEngine(t, storage)
	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "10",
		PackageIDs:  []string{"pkg-food"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "test",
			TaxonomyID:     1,
			Topics:         []string{"food.cooking"},
		},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "publisher-provided topics under accepted taxonomy must activate")
}

func TestContext_TopicTargets_EmptyAcceptedTaxonomies_FailClosed(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-food", TopicTargets: true})

	engine := newEngine(t, storage, func(cfg *targeting.ContextEngineConfig) {
		cfg.AcceptedTaxonomies = nil
	})

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: "10", PackageIDs: []string{"pkg-food"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "TopicTargets package must fail-closed with no taxonomies configured")
}

func TestContext_RogueTaxonomyOnlySource_FailClosed(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-food", TopicTargets: true}).
		WithPackageTopics(testTaxonomy, "pkg-food", []string{"food.cooking"})

	engine := newEngine(t, storage)
	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "10",
		PackageIDs:  []string{"pkg-food"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "rogue",
			TaxonomyID:     99,
			Topics:         []string{"food.cooking"},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers,
		"rogue ContextSignals taxonomy with no artifact refs must fail-closed (publisher attempted a topic source but engine couldn't use it)")
}

func TestContext_URLBlocklist(t *testing.T) {
	urlHash := targeting.HashURL("article:bad")
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-family", URLBlocklist: true}).
		WithURLBlocked("pkg-family", urlHash)

	engine := newEngine(t, storage)
	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "10",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:bad"}},
		PackageIDs:   []string{"pkg-family"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestContext_URLAllowlist(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-curated", URLAllowlist: true}).
		WithURLAllowed("pkg-curated", targeting.HashURL("article:safe"))

	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "10",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:safe"}},
		PackageIDs:   []string{"pkg-curated"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)

	resp, err = engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "r",
		PropertyRID:  "10",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:not-allowed"}},
		PackageIDs:   []string{"pkg-curated"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestContext_PerPackagePropertyTargeting(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{
			PackageID:    "pkg-scoped",
			PropertyRIDs: []string{"20"},
		})

	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: "10", PackageIDs: []string{"pkg-scoped"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "property not in per-package list → no match")

	resp, err = engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: "20", PackageIDs: []string{"pkg-scoped"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}

func TestContext_EmitSegments(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{
			PackageID:    "pkg-1",
			EmitSegments: []string{"food", "premium"},
		})

	engine := newEngine(t, storage)
	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: "10", PackageIDs: []string{"pkg-1"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Signals)
	segs, ok := resp.Signals["segments"].([]string)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"food", "premium"}, segs)
}

// TestContext_DefensiveCopiesAcceptedTaxonomies pins that
// post-construction mutation of the caller's slice cannot reach the
// engine's state.
func TestContext_DefensiveCopiesAcceptedTaxonomies(t *testing.T) {
	caller := []topicstore.Taxonomy{{Source: "iab", ID: 7}}
	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID:         testProviderID,
		SellerAgentURL:     testSeller,
		Properties:         targeting.PropertyList{Global: targeting.NewMapBitmap("10")},
		Storage:            contextstorage.NewInMemory(),
		AcceptedTaxonomies: caller,
	})
	caller[0] = topicstore.Taxonomy{Source: "rogue", ID: 99}

	// Indirect verification: a request whose ContextSignals declares
	// "iab:7" must still activate a TopicTargets package, because the
	// engine has its own copy.
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-1", TopicTargets: true}).
		WithPackageTopics(topicstore.Taxonomy{Source: "iab", ID: 7}, "pkg-1", []string{"632"})
	engine = targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID:         testProviderID,
		SellerAgentURL:     testSeller,
		Properties:         targeting.PropertyList{Global: targeting.NewMapBitmap("10")},
		Storage:            storage,
		AcceptedTaxonomies: caller, // caller mutated to "rogue:99"
	})
	caller[0] = topicstore.Taxonomy{Source: "iab", ID: 7} // change back AFTER construction

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "10",
		PackageIDs:  []string{"pkg-1"},
		ContextSignals: &tmproto.ContextSignals{
			TaxonomySource: "iab", TaxonomyID: 7, Topics: []string{"632"},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers,
		"engine must hold its own copy of AcceptedTaxonomies; rogue:99 was configured at construction time")
}

func TestContext_NowOverrideUsedForActivePackages(t *testing.T) {
	// Verifies the engine forwards its Now() output to
	// storage.ActivePackages. The in-memory storage doesn't model dates
	// (its ActivePackages key ignores `now`), so we use a custom
	// storage that asserts the passed time.
	probe := &nowProbeStorage{InMemory: *contextstorage.NewInMemory()}
	probe.WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-1"})

	frozen := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID:         testProviderID,
		SellerAgentURL:     testSeller,
		Properties:         targeting.PropertyList{Global: targeting.NewMapBitmap("10")},
		Storage:            probe,
		AcceptedTaxonomies: []topicstore.Taxonomy{testTaxonomy},
		Now:                func() time.Time { return frozen },
	})

	_, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: "10",
	})
	require.NoError(t, err)
	assert.Equal(t, frozen, probe.lastNow)
}

type nowProbeStorage struct {
	contextstorage.InMemory
	lastNow time.Time
}

func (p *nowProbeStorage) ActivePackages(ctx context.Context, sellerAgentURL, propertyID, country string, now time.Time) ([]string, error) {
	p.lastNow = now
	return p.InMemory.ActivePackages(ctx, sellerAgentURL, propertyID, country, now)
}
