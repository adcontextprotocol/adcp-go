package targeting_test

import (
	"context"
	"testing"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/contextstorage"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Direct-match tests cover the PackageContextConfig fields that match
// request values directly (geo, content identifiers, language,
// sentiment, keywords, content_policies), bypassing the signal
// indirection. The fields are an alternative to expressing buyer-typed
// value targeting through ContextSignals cfgs: when the publisher
// already sends the matched value on the request, a direct equality
// check avoids a tautological signal lookup.

const directMatchRID = "10"

func directMatchPkg(cfg *targeting.PackageContextConfig) *targeting.PackageContextConfig {
	cfg.PackageID = "pkg-direct"
	return cfg
}

func directMatchRequest(req *tmproto.ContextMatchRequest) *tmproto.ContextMatchRequest {
	req.RequestID = "r"
	req.PropertyRID = directMatchRID
	req.PackageIDs = []string{"pkg-direct"}
	return req
}

func TestDirectMatch_NoFieldsSet_PassesAnyRequest(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "US", "region": "US-NY"},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "empty direct-match fields impose no constraint")
}

// Geo.

func TestDirectMatch_Countries_Include(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Countries: []string{"US", "CA"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "US"},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "MX"},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "country not in include list rejects")
}

func TestDirectMatch_Countries_IncludeRejectsMissingValue(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Countries: []string{"US"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "a buyer that asked for any specific country must not match a request that supplied none")
}

func TestDirectMatch_CountriesExclude(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			CountriesExclude: []string{"RU", "CN"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "RU"},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "US"},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}

func TestDirectMatch_Regions(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Regions: []string{"US-CA"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "US", "region": "US-CA"},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "US", "region": "US-NY"},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestDirectMatch_Metros_SystemAndValue(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Metros: []targeting.MetroTarget{
				{System: "nielsen_dma", Values: []string{"501", "803"}},
				{System: "uk_itl2", Values: []string{"UKI3"}},
			},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{
			"country": "US",
			"metro":   map[string]any{"system": "nielsen_dma", "value": "501"},
		},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "matching system + value passes")

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{
			"country": "US",
			"metro":   map[string]any{"system": "nielsen_dma", "value": "777"},
		},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "value not in system's list rejects")

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{
			"country": "GB",
			"metro":   map[string]any{"system": "uk_itl2", "value": "UKI3"},
		},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "different-system entry in same include list passes for that system")
}

func TestDirectMatch_Metros_SystemMismatchRejects(t *testing.T) {
	// A package targeting nielsen_dma should NOT match a request the
	// publisher classified under uk_itl2 even if a value collision
	// existed — the engine matches on (system, value), not value alone.
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Metros: []targeting.MetroTarget{
				{System: "nielsen_dma", Values: []string{"501"}},
			},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{
			"country": "GB",
			"metro":   map[string]any{"system": "uk_itl2", "value": "501"},
		},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestDirectMatch_MetrosExclude(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			MetrosExclude: []targeting.MetroTarget{
				{System: "nielsen_dma", Values: []string{"555"}},
			},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{
			"country": "US",
			"metro":   map[string]any{"system": "nielsen_dma", "value": "555"},
		},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

// Geo hierarchy resolution: exclusion at a higher level takes
// precedence over inclusion at a more specific level.

func TestDirectMatch_HierarchyResolution_CountryExcludeBeatsRegionInclude(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			CountriesExclude: []string{"US"},
			Regions:          []string{"US-CA"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{"country": "US", "region": "US-CA"},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "country exclusion must short-circuit before region inclusion is evaluated")
}

func TestDirectMatch_HierarchyResolution_CountryExcludeBeatsMetroInclude(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			CountriesExclude: []string{"US"},
			Metros: []targeting.MetroTarget{
				{System: "nielsen_dma", Values: []string{"501"}},
			},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{
			"country": "US",
			"metro":   map[string]any{"system": "nielsen_dma", "value": "501"},
		},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "country exclusion must short-circuit before metro inclusion is evaluated")
}

func TestDirectMatch_HierarchyResolution_RegionExcludeBeatsMetroInclude(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			RegionsExclude: []string{"US-CA"},
			Metros: []targeting.MetroTarget{
				{System: "nielsen_dma", Values: []string{"803"}},
			},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo: map[string]any{
			"country": "US",
			"region":  "US-CA",
			"metro":   map[string]any{"system": "nielsen_dma", "value": "803"},
		},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "region exclusion must short-circuit before metro inclusion is evaluated")
}

// ContextSignals scalar/list fields.

func TestDirectMatch_Languages(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Languages: []string{"en", "fr"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{Language: "en"},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{Language: "de"},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestDirectMatch_Sentiments(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			SentimentsExclude: []string{"negative"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{Sentiment: "negative"},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{Sentiment: "positive"},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}

func TestDirectMatch_Keywords_Intersection(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Keywords: []string{"cooking", "recipes"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{Keywords: []string{"food", "recipes", "dinner"}},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "any-overlap matches inclusion")

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{Keywords: []string{"news", "politics"}},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestDirectMatch_KeywordsExclude(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			KeywordsExclude: []string{"violence"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{Keywords: []string{"news", "violence", "war"}},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "any-overlap with exclude rejects")
}

func TestDirectMatch_ContentPolicies(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			ContentPolicies: []string{"csbs"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{ContentPolicies: []string{"csbs", "iab-bs"}},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ContextSignals: &tmproto.ContextSignals{ContentPolicies: []string{"other"}},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

// Content-artifact identifiers.

func TestDirectMatch_EIDRs_FiltersToCorrectRefType(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			EIDRs: []string{"10.5240/AAAA", "10.5240/BBBB"},
		}))
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ArtifactRefs: []tmproto.ArtifactRef{
			{Type: tmproto.ArtifactRefTypeGTIN, Value: "10.5240/AAAA"}, // wrong type, should not match
			{Type: tmproto.ArtifactRefTypeEIDR, Value: "10.5240/AAAA"},
		},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)

	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		ArtifactRefs: []tmproto.ArtifactRef{
			{Type: tmproto.ArtifactRefTypeEIDR, Value: "10.5240/CCCC"},
		},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestDirectMatch_AllIdentifierTypes(t *testing.T) {
	// Each identifier list is matched against ArtifactRefs of the
	// corresponding Type independently — confirm the type mapping for
	// every supported list.
	cases := []struct {
		name    string
		setCfg  func(*targeting.PackageContextConfig)
		refType tmproto.ArtifactRefType
	}{
		{"gracenote", func(c *targeting.PackageContextConfig) { c.Gracenotes = []string{"SH123"} }, tmproto.ArtifactRefTypeGracenote},
		{"isrc", func(c *targeting.PackageContextConfig) { c.ISRCs = []string{"USRC17607839"} }, tmproto.ArtifactRefTypeISRC},
		{"gtin", func(c *targeting.PackageContextConfig) { c.GTINs = []string{"00012345"} }, tmproto.ArtifactRefTypeGTIN},
		{"rss_guid", func(c *targeting.PackageContextConfig) { c.RSSGUIDs = []string{"guid-1"} }, tmproto.ArtifactRefTypeRSSGUID},
		{"isbn", func(c *targeting.PackageContextConfig) { c.ISBNs = []string{"978-0-12345-678-9"} }, tmproto.ArtifactRefTypeISBN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &targeting.PackageContextConfig{PackageID: "pkg-direct"}
			tc.setCfg(cfg)
			storage := contextstorage.NewInMemory().WithPackage(cfg)
			engine := newEngine(t, storage)

			// Matching ref type + value → match.
			refValue := ""
			switch tc.refType {
			case tmproto.ArtifactRefTypeGracenote:
				refValue = "SH123"
			case tmproto.ArtifactRefTypeISRC:
				refValue = "USRC17607839"
			case tmproto.ArtifactRefTypeGTIN:
				refValue = "00012345"
			case tmproto.ArtifactRefTypeRSSGUID:
				refValue = "guid-1"
			case tmproto.ArtifactRefTypeISBN:
				refValue = "978-0-12345-678-9"
			}
			resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
				ArtifactRefs: []tmproto.ArtifactRef{{Type: tc.refType, Value: refValue}},
			}))
			require.NoError(t, err)
			assert.Len(t, resp.Offers, 1)

			// Different ref type with the same value → no match.
			resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
				ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeCustom, Value: refValue}},
			}))
			require.NoError(t, err)
			assert.Empty(t, resp.Offers, "ref of wrong type must not match a typed identifier list")
		})
	}
}

// Independence: non-geo fields evaluate independently — they don't
// gain or lose precedence from each other.

func TestDirectMatch_IndependentGates_AllMustPass(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(directMatchPkg(&targeting.PackageContextConfig{
			Countries: []string{"US"},
			Languages: []string{"en"},
		}))
	engine := newEngine(t, storage)

	// Country passes, language fails → package rejected.
	resp, err := engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo:            map[string]any{"country": "US"},
		ContextSignals: &tmproto.ContextSignals{Language: "fr"},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)

	// Both pass → package matches.
	resp, err = engine.Evaluate(context.Background(), directMatchRequest(&tmproto.ContextMatchRequest{
		Geo:            map[string]any{"country": "US"},
		ContextSignals: &tmproto.ContextSignals{Language: "en"},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}
