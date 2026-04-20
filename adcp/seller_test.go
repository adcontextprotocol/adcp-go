package adcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCapabilitiesRequiresIdempotencyTTL(t *testing.T) {
	// Zero TTL must panic — 3.0 schema requires replay_ttl_seconds.
	assert.Panics(t, func() {
		buildCapabilities(Config{
			GetProducts: func(context.Context, any, *GetProductsRequest) (*ProductsData, error) { return nil, nil },
		})
	}, "zero TTL should panic")

	// Below minimum (1h) must panic.
	assert.Panics(t, func() {
		buildCapabilities(Config{IdempotencyReplayTTL: 30 * time.Minute})
	}, "below 1h should panic")

	// Above maximum (7d) must panic.
	assert.Panics(t, func() {
		buildCapabilities(Config{IdempotencyReplayTTL: 8 * 24 * time.Hour})
	}, "above 7d should panic")
}

func TestBuildCapabilitiesPanicsOnEmptyProtocols(t *testing.T) {
	// No handlers + no Capabilities override = empty supported_protocols,
	// which fails the 3.0 schema (minItems: 1).
	assert.Panics(t, func() {
		buildCapabilities(Config{IdempotencyReplayTTL: 24 * time.Hour})
	}, "empty supported_protocols should panic")
}

func TestBuildCapabilitiesPanicsOnTTLConflict(t *testing.T) {
	// Caller supplied a different TTL via Capabilities — Config.TTL wins but
	// we panic so the caller doesn't get silently-overwritten behavior.
	assert.Panics(t, func() {
		buildCapabilities(Config{
			IdempotencyReplayTTL: 24 * time.Hour,
			Capabilities: &CapabilitiesData{
				SupportedProtocols: []string{"media_buy"},
				ADCP: &ADCPVersion{
					MajorVersions: []int{3},
					Idempotency:   IdempotencyCaps{ReplayTTLSeconds: 3600},
				},
			},
		})
	}, "mismatched TTL between Config and Capabilities should panic")
}

func TestBuildCapabilitiesDefaults(t *testing.T) {
	caps := buildCapabilities(Config{
		IdempotencyReplayTTL: 24 * time.Hour,
		GetProducts: func(context.Context, any, *GetProductsRequest) (*ProductsData, error) {
			return nil, nil
		},
	})

	require.NotNil(t, caps.ADCP)
	assert.Equal(t, 86400, caps.ADCP.Idempotency.ReplayTTLSeconds)
	assert.Equal(t, []int{3}, caps.ADCP.MajorVersions)
	assert.Contains(t, caps.SupportedProtocols, "media_buy")
}

func TestBuildCapabilitiesFillsMajorVersions(t *testing.T) {
	caps := buildCapabilities(Config{
		IdempotencyReplayTTL: 24 * time.Hour,
		Capabilities: &CapabilitiesData{
			SupportedProtocols: []string{"media_buy"},
			ADCP:               &ADCPVersion{}, // empty MajorVersions
		},
	})
	assert.Equal(t, []int{3}, caps.ADCP.MajorVersions)
}

func TestBuildCapabilitiesPreservesCallerBlocks(t *testing.T) {
	caps := buildCapabilities(Config{
		IdempotencyReplayTTL: 1 * time.Hour,
		Capabilities: &CapabilitiesData{
			SupportedProtocols: []string{"media_buy"},
			Account: &AccountCapabilities{
				SupportedBilling: []string{"agent"},
			},
			MediaBuy: &MediaBuyCapabilities{
				Portfolio: &PortfolioCaps{PublisherDomains: []string{"example.com"}},
			},
		},
	})

	require.NotNil(t, caps.Account)
	assert.Equal(t, []string{"agent"}, caps.Account.SupportedBilling)
	require.NotNil(t, caps.MediaBuy)
	require.NotNil(t, caps.MediaBuy.Portfolio)
	assert.Equal(t, []string{"example.com"}, caps.MediaBuy.Portfolio.PublisherDomains)
	assert.Equal(t, 3600, caps.ADCP.Idempotency.ReplayTTLSeconds)
}

func TestDetectProtocolsEmitsOnlySchemaEnum(t *testing.T) {
	// Every value returned from detectProtocols must be in the 3.0
	// supported_protocols enum. Regression guard against accidentally
	// emitting an invalid value.
	valid := map[string]bool{
		"media_buy": true, "signals": true, "governance": true,
		"sponsored_intelligence": true, "creative": true, "brand": true,
	}
	got := detectProtocols(Config{
		GetProducts:          func(context.Context, any, *GetProductsRequest) (*ProductsData, error) { return nil, nil },
		GetSignals:           func(context.Context, *GetSignalsRequest) ([]Signal, error) { return nil, nil },
		CreateCollectionList: func(context.Context, *CreateCollectionListRequest) (*CreateCollectionListResult, error) { return nil, nil },
	})
	for _, p := range got {
		assert.Truef(t, valid[p], "detectProtocols returned %q which is not in the 3.0 supported_protocols enum", p)
	}
}

func TestCapabilitiesResponseWireShape(t *testing.T) {
	// Round-trip through JSON to verify the 3.0 wire shape: adcp.idempotency
	// must be present as an object (not null), and media_buy blocks survive.
	result, _, err := CapabilitiesResponse(buildCapabilities(Config{
		IdempotencyReplayTTL: 24 * time.Hour,
		Capabilities: &CapabilitiesData{
			SupportedProtocols: []string{"media_buy"},
			MediaBuy: &MediaBuyCapabilities{
				SupportedPricingModels: []string{"cpm"},
			},
		},
	}))
	require.NoError(t, err)
	require.NotNil(t, result)

	b, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(b, &wire))

	adcp, ok := wire["adcp"].(map[string]any)
	require.True(t, ok, "adcp block should be an object")
	idem, ok := adcp["idempotency"].(map[string]any)
	require.True(t, ok, "adcp.idempotency must be present as an object (required in 3.0)")
	assert.EqualValues(t, 86400, idem["replay_ttl_seconds"])

	mb, ok := wire["media_buy"].(map[string]any)
	require.True(t, ok)
	models, _ := mb["supported_pricing_models"].([]any)
	assert.Equal(t, []any{"cpm"}, models)
}
