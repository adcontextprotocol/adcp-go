package targeting_test

import (
	"context"
	"errors"
	"testing"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/contextstorage"
	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errSignalsStorage wraps the in-memory storage to force SignalMGet
// to return an error, simulating a Valkey outage on the signal:* key
// space. Every other dimension is delegated to the wrapped storage.
type errSignalsStorage struct {
	*contextstorage.InMemory
	err error
}

func (s *errSignalsStorage) SignalMGet(_ context.Context, _ ...string) ([]string, error) {
	return nil, s.err
}

const signalsTestRID = "10"

func signalProfile(any_ []signalstore.Cfg, none []signalstore.Cfg) *signalstore.Profile {
	return &signalstore.Profile{AnyOf: any_, NoneOf: none}
}

func TestContext_SignalAnyOf_MatchesActivates(t *testing.T) {
	cfg := &targeting.PackageContextConfig{
		PackageID: "pkg-a",
		ContextSignals: signalProfile(
			[]signalstore.Cfg{{
				SignalOwnerID: 7,
				KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeCountry},
				SignalID:      "us-traffic",
			}},
			nil,
		),
	}
	storage := contextstorage.NewInMemory().
		WithPackage(cfg).
		WithSignalValue(
			signalstore.Key(7, []signalstore.KeyType{signalstore.KeyTypeCountry}, []string{"US"}),
			"us-traffic",
		)
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
		Geo: map[string]any{"country": "US"},
	})
	require.NoError(t, err)
	require.Len(t, resp.Offers, 1)
}

func TestContext_SignalAnyOf_NoMatchSkips(t *testing.T) {
	cfg := &targeting.PackageContextConfig{
		PackageID: "pkg-a",
		ContextSignals: signalProfile(
			[]signalstore.Cfg{{
				SignalOwnerID: 7,
				KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeCountry},
				SignalID:      "premium",
			}},
			nil,
		),
	}
	// Seeded key returns "other-segment" — does not match cfg.SignalID.
	storage := contextstorage.NewInMemory().
		WithPackage(cfg).
		WithSignalValue(
			signalstore.Key(7, []signalstore.KeyType{signalstore.KeyTypeCountry}, []string{"US"}),
			"other-segment",
		)
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
		Geo: map[string]any{"country": "US"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestContext_SignalNoneOf_BlocksMatch(t *testing.T) {
	cfg := &targeting.PackageContextConfig{
		PackageID: "pkg-a",
		ContextSignals: signalProfile(
			nil,
			[]signalstore.Cfg{{
				SignalOwnerID: 9,
				KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeCountry},
				SignalID:      "blocked-geo",
			}},
		),
	}
	storage := contextstorage.NewInMemory().
		WithPackage(cfg).
		WithSignalValue(
			signalstore.Key(9, []signalstore.KeyType{signalstore.KeyTypeCountry}, []string{"US"}),
			"blocked-geo",
		)
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
		Geo: map[string]any{"country": "US"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "none_of match must reject the package")
}

func TestContext_SignalInvalidKeyType_FailsClosed(t *testing.T) {
	cfg := &targeting.PackageContextConfig{
		PackageID: "pkg-a",
		ContextSignals: signalProfile(
			[]signalstore.Cfg{{
				SignalOwnerID: 1,
				KeyTypes:      []signalstore.KeyType{"eid"}, // identity key, rejected
				SignalID:      "audience-x",
			}},
			nil,
		),
	}
	storage := contextstorage.NewInMemory().WithPackage(cfg)
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "invalid key_type in profile must fail-closed for the package")
}

func TestContext_SignalDedupAcrossPackages(t *testing.T) {
	// Two packages target the same (owner, key_type, value, signal_id).
	// Only one MGet should be issued, both packages should pass.
	shared := signalstore.Cfg{
		SignalOwnerID: 5,
		KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeCountry},
		SignalID:      "co-shared",
	}
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{
			PackageID:      "pkg-a",
			ContextSignals: signalProfile([]signalstore.Cfg{shared}, nil),
		}).
		WithPackage(&targeting.PackageContextConfig{
			PackageID:      "pkg-b",
			ContextSignals: signalProfile([]signalstore.Cfg{shared}, nil),
		}).
		WithSignalValue(
			signalstore.Key(5, []signalstore.KeyType{signalstore.KeyTypeCountry}, []string{"US"}),
			"co-shared",
		)
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID,
		PackageIDs: []string{"pkg-a", "pkg-b"},
		Geo:        map[string]any{"country": "US"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 2, "both packages share the same key and should both pass off one MGet")
}

func TestContext_SignalProfileEmptyPasses(t *testing.T) {
	storage := contextstorage.NewInMemory().
		WithPackage(&targeting.PackageContextConfig{PackageID: "pkg-a"})
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}

func TestContext_SignalMGetError_FailsClosed(t *testing.T) {
	// Every package with a profile must be dropped when the
	// underlying signal MGet fails; packages without a profile
	// continue to evaluate.
	gated := &targeting.PackageContextConfig{
		PackageID: "pkg-gated",
		ContextSignals: signalProfile(
			[]signalstore.Cfg{{
				SignalOwnerID: 1,
				KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeCountry},
				SignalID:      "us-traffic",
			}},
			nil,
		),
	}
	bare := &targeting.PackageContextConfig{PackageID: "pkg-bare"}
	mem := contextstorage.NewInMemory().WithPackage(gated).WithPackage(bare)
	storage := &errSignalsStorage{InMemory: mem, err: errors.New("simulated valkey outage")}
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID,
		PackageIDs: []string{"pkg-gated", "pkg-bare"},
		Geo:        map[string]any{"country": "US"},
	})
	require.NoError(t, err)
	require.Len(t, resp.Offers, 1, "only the bare package (no profile) should activate when MGet fails")
}

func TestContext_SignalTopicTaxonomyNamespacing(t *testing.T) {
	// The writer keys topic values as `{source}:{id}:{topicID}` so a
	// cfg gated on topic "632" under iab:7 sees the right bytes
	// only when the engine's accepted taxonomies include iab:7. A
	// publisher whose declared taxonomy is unaccepted gets the
	// topics silently dropped (matches the topic-match path).
	cfg := &targeting.PackageContextConfig{
		PackageID: "pkg-a",
		ContextSignals: signalProfile(
			[]signalstore.Cfg{{
				SignalOwnerID: 7,
				KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeTopic},
				SignalID:      "sports",
			}},
			nil,
		),
	}
	iab := topicstore.Taxonomy{Source: "iab", ID: 7}
	storage := contextstorage.NewInMemory().
		WithPackage(cfg).
		WithSignalValue(
			signalstore.Key(7, []signalstore.KeyType{signalstore.KeyTypeTopic}, []string{"iab:7:632"}),
			"sports",
		)
	engine := newEngine(t, storage, func(c *targeting.ContextEngineConfig) {
		c.AcceptedTaxonomies = []topicstore.Taxonomy{iab, testTaxonomy}
	})

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
		ContextSignals: &tmproto.ContextSignals{
			Topics:         []string{"632"},
			TaxonomySource: "iab",
			TaxonomyID:     7,
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.Offers, 1, "accepted taxonomy + namespaced topic match must activate")
}

func TestContext_SignalTopicUnacceptedTaxonomyDrops(t *testing.T) {
	cfg := &targeting.PackageContextConfig{
		PackageID: "pkg-a",
		ContextSignals: signalProfile(
			[]signalstore.Cfg{{
				SignalOwnerID: 7,
				KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeTopic},
				SignalID:      "sports",
			}},
			nil,
		),
	}
	// Engine only accepts "test:1"; publisher declares iab:7. The
	// topic should drop, the cfg has no data to match against, and
	// the any_of fails → package skipped.
	storage := contextstorage.NewInMemory().
		WithPackage(cfg).
		WithSignalValue(
			signalstore.Key(7, []signalstore.KeyType{signalstore.KeyTypeTopic}, []string{"iab:7:632"}),
			"sports",
		)
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
		ContextSignals: &tmproto.ContextSignals{
			Topics:         []string{"632"},
			TaxonomySource: "iab",
			TaxonomyID:     7,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "topics from unaccepted taxonomy must not contribute to signal lookup")
}

func TestContext_SignalURLHashFromRawURL(t *testing.T) {
	// `url` artifact refs must project onto KeyTypeURLHash so the
	// writer (which only keys hashes) is reachable from publishers
	// that send raw URLs. The hash is the spec-canonical
	// Blake3+base64.
	rawURL := "https://example.com/article/42"
	hash := tmproto.HashURL(rawURL)
	cfg := &targeting.PackageContextConfig{
		PackageID: "pkg-a",
		ContextSignals: signalProfile(
			[]signalstore.Cfg{{
				SignalOwnerID: 7,
				KeyTypes:      []signalstore.KeyType{signalstore.KeyTypeURLHash},
				SignalID:      "premium-content",
			}},
			nil,
		),
	}
	storage := contextstorage.NewInMemory().
		WithPackage(cfg).
		WithSignalValue(
			signalstore.Key(7, []signalstore.KeyType{signalstore.KeyTypeURLHash}, []string{hash}),
			"premium-content",
		)
	engine := newEngine(t, storage)

	resp, err := engine.Evaluate(context.Background(), &tmproto.ContextMatchRequest{
		RequestID: "r", PropertyRID: signalsTestRID, PackageIDs: []string{"pkg-a"},
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: rawURL}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Offers, 1, "raw url should hash and match the url_hash-keyed signal")
}
