package adcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		GetProducts: func(context.Context, any, *GetProductsRequest) (*ProductsData, error) { return nil, nil },
		GetSignals:  func(context.Context, *GetSignalsRequest) ([]Signal, error) { return nil, nil },
		CreateCollectionList: func(context.Context, *CreateCollectionListRequest) (*CreateCollectionListResult, error) {
			return nil, nil
		},
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

func TestAttachContext(t *testing.T) {
	t.Run("nil result", func(t *testing.T) {
		assert.Nil(t, attachContext(nil, map[string]any{"trace_id": "ctx-1"}))
	})

	t.Run("nil context", func(t *testing.T) {
		result := buildResult("ok", map[string]any{"status": "ok"})

		got := attachContext(result, nil)

		require.Same(t, result, got)
		assert.NotContains(t, structuredContentMap(t, got), "context")
	})

	t.Run("adds context", func(t *testing.T) {
		ctxValue := map[string]any{"trace_id": "ctx-1", "retry": false}
		result := buildResult("ok", map[string]any{"status": "ok"})

		got := attachContext(result, ctxValue)

		require.Same(t, result, got)
		assert.Equal(t, ctxValue, structuredContentMap(t, got)["context"])
	})
}

func TestRegisteredHandlersAttachContext(t *testing.T) {
	ctxValue := map[string]any{"trace_id": "ctx-1", "retry": false}
	args := map[string]any{"context": ctxValue}

	tests := []struct {
		name string
		tool string
		args map[string]any
		cfg  Config
	}{
		{
			name: "media buy success",
			tool: "get_products",
			args: map[string]any{"buying_mode": "brief", "context": ctxValue},
			cfg: baseTestConfig(Config{
				GetProducts: func(context.Context, any, *GetProductsRequest) (*ProductsData, error) {
					return &ProductsData{Products: []Product{}}, nil
				},
			}),
		},
		{
			name: "media buy error",
			tool: "get_products",
			args: map[string]any{"buying_mode": "brief", "context": ctxValue},
			cfg: baseTestConfig(Config{
				GetProducts: func(context.Context, any, *GetProductsRequest) (*ProductsData, error) {
					return nil, errors.New("boom")
				},
			}),
		},
		{
			name: "signals success",
			tool: "get_signals",
			args: args,
			cfg: baseTestConfig(Config{
				GetSignals: func(context.Context, *GetSignalsRequest) ([]Signal, error) {
					return []Signal{}, nil
				},
			}),
		},
		{
			name: "signals error",
			tool: "get_signals",
			args: args,
			cfg: baseTestConfig(Config{
				GetSignals: func(context.Context, *GetSignalsRequest) ([]Signal, error) {
					return nil, errors.New("boom")
				},
			}),
		},
		{
			name: "collection success",
			tool: "list_collection_lists",
			args: args,
			cfg: baseTestConfig(Config{
				ListCollectionLists: func(context.Context, *ListCollectionListsRequest) (*ListCollectionListsResult, error) {
					return &ListCollectionListsResult{Lists: []CollectionList{}}, nil
				},
			}),
		},
		{
			name: "collection error",
			tool: "list_collection_lists",
			args: args,
			cfg: baseTestConfig(Config{
				ListCollectionLists: func(context.Context, *ListCollectionListsRequest) (*ListCollectionListsResult, error) {
					return nil, errors.New("boom")
				},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := callRegisteredTool(t, tt.cfg, tt.tool, tt.args)

			assert.Equal(t, ctxValue, structuredContentMap(t, result)["context"])
		})
	}
}

func baseTestConfig(cfg Config) Config {
	cfg.IdempotencyReplayTTL = 24 * time.Hour
	if cfg.Capabilities == nil {
		cfg.Capabilities = &CapabilitiesData{SupportedProtocols: []string{"media_buy"}}
	}
	return cfg
}

func callRegisteredTool(t *testing.T, cfg Config, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0.0.1"}, nil)
	Register(server, cfg)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "seller-test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer clientSession.Close()

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func structuredContentMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	require.NotNil(t, result)
	m, ok := jsonRoundTrip(result.StructuredContent).(map[string]any)
	require.Truef(t, ok, "expected structured content map, got %T", result.StructuredContent)
	return m
}
