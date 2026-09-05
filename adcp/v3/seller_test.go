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
	assert.Equal(t, []string{"3.0", "3.1", "3.2-rc.1"}, caps.ADCP.SupportedVersions)
	assert.Equal(t, "3.2-rc.1", caps.AdcpVersion)
	assert.Equal(t, 3, caps.AdcpMajorVersion)
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

func TestBuildCapabilitiesPreservesCallerSupportedVersions(t *testing.T) {
	caps := buildCapabilities(Config{
		IdempotencyReplayTTL: 24 * time.Hour,
		Capabilities: &CapabilitiesData{
			SupportedProtocols: []string{"media_buy"},
			ADCP: &ADCPVersion{
				MajorVersions:     []int{3},
				SupportedVersions: []string{"3.0"},
			},
		},
	})

	assert.Equal(t, []string{"3.0"}, caps.ADCP.SupportedVersions)
	assert.Equal(t, "3.0", caps.AdcpVersion)
	assert.Equal(t, 3, caps.AdcpMajorVersion)
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

func TestDetectProtocolsCoversRegisteredProtocolHandlers(t *testing.T) {
	got := detectProtocols(Config{
		SyncGovernance: func(context.Context, *SyncGovernanceRequest) ([]GovernanceResult, error) { return nil, nil },
		SyncCreatives:  func(context.Context, *SyncCreativesRequest) ([]CreativeResult, error) { return nil, nil },
	})

	assert.ElementsMatch(t, []string{"governance", "creative"}, got)
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
	assert.Equal(t, []any{"3.0", "3.1", "3.2-rc.1"}, adcp["supported_versions"])
	assert.Equal(t, "3.2-rc.1", wire["adcp_version"])
	assert.EqualValues(t, 3, wire["adcp_major_version"])

	mb, ok := wire["media_buy"].(map[string]any)
	require.True(t, ok)
	models, _ := mb["supported_pricing_models"].([]any)
	assert.Equal(t, []any{"cpm"}, models)
}

func TestCapabilitiesResponseDoesNotMutateInputStatus(t *testing.T) {
	data := &CapabilitiesData{
		SupportedProtocols: []string{"media_buy"},
		ADCP:               &ADCPVersion{MajorVersions: []int{3}},
	}

	_, out, err := CapabilitiesResponse(data)
	require.NoError(t, err)

	assert.Empty(t, data.Status)
	require.IsType(t, &CapabilitiesData{}, out)
	assert.Equal(t, "completed", out.(*CapabilitiesData).Status)
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

func TestRegisteredCapabilitiesUsesDefaultVersion(t *testing.T) {
	result := callRegisteredTool(t, baseTestConfig(Config{}), "get_adcp_capabilities", map[string]any{})
	wire := structuredContentMap(t, result)

	assert.False(t, result.IsError)
	assert.Equal(t, "3.2-rc.1", wire["adcp_version"])
	assert.EqualValues(t, 3, wire["adcp_major_version"])
	adcpBlock, ok := wire["adcp"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"3.0", "3.1", "3.2-rc.1"}, adcpBlock["supported_versions"])
}

func TestRegisteredCapabilitiesFiltersProtocols(t *testing.T) {
	result := callRegisteredTool(t, baseTestConfig(Config{
		Capabilities: &CapabilitiesData{
			SupportedProtocols: []string{"media_buy", "signals", "creative"},
			Account:            &AccountCapabilities{SupportedBilling: []string{"agent"}},
			MediaBuy:           &MediaBuyCapabilities{SupportedPricingModels: []string{"cpm"}},
			Signals:            &SignalsCapabilities{DiscoveryModes: []string{"managed"}},
			Creative:           &CreativeCapabilities{HasCreativeLibrary: Bool(true)},
		},
	}), "get_adcp_capabilities", map[string]any{"protocols": []string{"media_buy"}})

	wire := structuredContentMap(t, result)
	assert.False(t, result.IsError)
	assert.Equal(t, []any{"media_buy"}, wire["supported_protocols"])
	assert.Contains(t, wire, "account")
	assert.Contains(t, wire, "media_buy")
	assert.NotContains(t, wire, "signals")
	assert.NotContains(t, wire, "creative")
}

func TestRegisteredCapabilitiesRejectsUnsupportedProtocolFilter(t *testing.T) {
	result := callRegisteredTool(t, baseTestConfig(Config{}), "get_adcp_capabilities", map[string]any{"protocols": []string{"signals"}})
	wire := structuredContentMap(t, result)

	assert.True(t, result.IsError)
	errPayload, ok := wire["adcp_error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "UNSUPPORTED_FEATURE", errPayload["code"])
	assert.Equal(t, "protocols", errPayload["field"])
}

func TestRegisteredCapabilitiesPreservesVersionPinRejection(t *testing.T) {
	result := callRegisteredTool(t, baseTestConfig(Config{}), "get_adcp_capabilities", map[string]any{
		"adcp_version": "9.9",
	})
	wire := structuredContentMap(t, result)

	assert.True(t, result.IsError)
	errPayload, ok := wire["adcp_error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "VERSION_UNSUPPORTED", errPayload["code"])
}

func TestRegisteredCreateMediaBuyStampsVariants(t *testing.T) {
	ctxValue := map[string]any{"trace_id": "ctx-1", "retry": false}
	args := map[string]any{"context": ctxValue}

	t.Run("success stamps sandbox and context", func(t *testing.T) {
		result := callRegisteredTool(t, baseTestConfig(Config{
			Sandbox: true,
			CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
				return &CreateMediaBuySuccess{
					MediaBuyID: "mb-1",
					Packages:   []Package{},
				}, nil
			},
		}), "create_media_buy", args)

		wire := structuredContentMap(t, result)
		assert.Equal(t, ctxValue, wire["context"])
		assert.Equal(t, true, wire["sandbox"])
	})

	t.Run("success value stamps sandbox and context", func(t *testing.T) {
		result := callRegisteredTool(t, baseTestConfig(Config{
			Sandbox: true,
			CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
				return CreateMediaBuySuccess{
					MediaBuyID: "mb-1",
					Packages:   []Package{},
				}, nil
			},
		}), "create_media_buy", args)

		wire := structuredContentMap(t, result)
		assert.Equal(t, ctxValue, wire["context"])
		assert.Equal(t, true, wire["sandbox"])
	})

	t.Run("success preserves explicit sandbox", func(t *testing.T) {
		result := callRegisteredTool(t, baseTestConfig(Config{
			Sandbox: true,
			CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
				return &CreateMediaBuySuccess{
					MediaBuyID: "mb-1",
					Packages:   []Package{},
					Sandbox:    Bool(false),
				}, nil
			},
		}), "create_media_buy", args)

		wire := structuredContentMap(t, result)
		assert.Equal(t, ctxValue, wire["context"])
		assert.Equal(t, false, wire["sandbox"])
	})

	t.Run("submitted stamps context", func(t *testing.T) {
		result := callRegisteredTool(t, baseTestConfig(Config{
			CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
				return &CreateMediaBuySubmitted{
					TaskID: "task-1",
				}, nil
			},
		}), "create_media_buy", args)

		wire := structuredContentMap(t, result)
		assert.Equal(t, ctxValue, wire["context"])
		assert.Equal(t, "submitted", wire["status"])
	})

	t.Run("schema error stamps context", func(t *testing.T) {
		result := callRegisteredTool(t, baseTestConfig(Config{
			CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
				return &CreateMediaBuyError{
					Errors: []AdcpError{{"code": "INVALID_REQUEST", "message": "bad request"}},
				}, nil
			},
		}), "create_media_buy", args)

		wire := structuredContentMap(t, result)
		assert.True(t, result.IsError)
		assert.Equal(t, ctxValue, wire["context"])
	})

	t.Run("schema error value stamps context", func(t *testing.T) {
		result := callRegisteredTool(t, baseTestConfig(Config{
			CreateMediaBuy: func(context.Context, any, *CreateMediaBuyRequest) (CreateMediaBuyResponse, error) {
				return CreateMediaBuyError{
					Errors: []AdcpError{{"code": "INVALID_REQUEST", "message": "bad request"}},
				}, nil
			},
		}), "create_media_buy", args)

		wire := structuredContentMap(t, result)
		assert.True(t, result.IsError)
		assert.Equal(t, ctxValue, wire["context"])
	})
}

func baseTestConfig(cfg Config) Config {
	cfg.IdempotencyReplayTTL = 24 * time.Hour
	if cfg.Capabilities == nil {
		cfg.Capabilities = &CapabilitiesData{SupportedProtocols: []string{"media_buy"}}
	}
	return cfg
}

func TestRegisteredRefineProposalsPreflightsBeforeMutation(t *testing.T) {
	handlerCalled := false
	result := callRegisteredTool(t, baseTestConfig(Config{
		RefineProposalsPreflight: func(context.Context, any, *RefineProposalsRequest) error {
			return NewError("INVALID_STATE", ErrorOptions{Message: "source proposal is unavailable"})
		},
		RefineProposals: func(context.Context, any, *RefineProposalsRequest) (*RefineProposalsData, error) {
			handlerCalled = true
			return nil, nil
		},
	}), "refine_proposals", map[string]any{
		"idempotency_key": "idem-preflight",
		"refinements":     []any{map[string]any{"proposal_id": "p-1", "action": "revise", "ask": "reduce the rate"}},
	})

	assert.True(t, result.IsError)
	assert.False(t, handlerCalled)
}

func TestRegisteredRefineProposalsDoesNotTreatContextAsAccount(t *testing.T) {
	resolverCalled := false
	handlerCalled := false
	result := callRegisteredTool(t, baseTestConfig(Config{
		ResolveAccount: func(context.Context, AccountReference) (any, error) {
			resolverCalled = true
			return nil, errors.New("context must not be resolved as an account")
		},
		RefineProposals: func(_ context.Context, acct any, _ *RefineProposalsRequest) (*RefineProposalsData, error) {
			handlerCalled = true
			assert.Nil(t, acct)
			return &RefineProposalsData{Status: "submitted", TaskID: "task-1"}, nil
		},
	}), "refine_proposals", map[string]any{
		"idempotency_key": "idem-context",
		"context":         map[string]any{"trace": "opaque"},
		"refinements":     []any{map[string]any{"proposal_id": "p-1", "action": "revise", "ask": "reduce the rate"}},
	})

	assert.False(t, result.IsError)
	assert.True(t, handlerCalled)
	assert.False(t, resolverCalled)
}

func TestRegisteredRefineProposalsUsesAtomicFinalizeWrapper(t *testing.T) {
	atomicCalled := false
	result := callRegisteredTool(t, baseTestConfig(Config{
		RefineProposals: func(context.Context, any, *RefineProposalsRequest) (*RefineProposalsData, error) {
			proposal := validNegotiationProposal(t, "committed-1", "draft-1", 500, 5)
			proposal.ProposalStatus = "committed"
			proposal.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			return &RefineProposalsData{Status: "completed", Results: []RefinementResult{{
				SourceProposalID: "draft-1", Outcome: OutcomeFinalized, Proposal: &proposal,
			}}}, nil
		},
		FinalizeProposalsAtomically: func(_ context.Context, _ any, _ *RefineProposalsRequest, execute func() (*RefineProposalsData, error)) (*RefineProposalsData, error) {
			atomicCalled = true
			return execute()
		},
	}), "refine_proposals", map[string]any{
		"idempotency_key": "idem-finalize",
		"refinements":     []any{map[string]any{"proposal_id": "draft-1", "action": "finalize"}},
	})

	assert.False(t, result.IsError)
	assert.True(t, atomicCalled)
}

func TestRegisteredRefineProposalsValidatesBeforeAtomicCommit(t *testing.T) {
	committed := false
	result := callRegisteredTool(t, baseTestConfig(Config{
		RefineProposals: func(context.Context, any, *RefineProposalsRequest) (*RefineProposalsData, error) {
			proposal := validNegotiationProposal(t, "draft-1", "draft-1", 500, 5)
			proposal.ProposalStatus = "committed"
			proposal.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			return &RefineProposalsData{Status: "completed", Results: []RefinementResult{{
				SourceProposalID: "draft-1", Outcome: OutcomeFinalized, Proposal: &proposal,
			}}}, nil
		},
		FinalizeProposalsAtomically: func(_ context.Context, _ any, _ *RefineProposalsRequest, execute func() (*RefineProposalsData, error)) (*RefineProposalsData, error) {
			data, err := execute()
			if err == nil {
				committed = true
			}
			return data, err
		},
	}), "refine_proposals", map[string]any{
		"idempotency_key": "idem-invalid-finalize",
		"refinements":     []any{map[string]any{"proposal_id": "draft-1", "action": "finalize"}},
	})

	assert.True(t, result.IsError)
	assert.False(t, committed, "invalid response must fail before transaction commit")
}

func TestRegisteredRefineProposalsRequiresAtomicFinalizeWrapper(t *testing.T) {
	handlerCalled := false
	result := callRegisteredTool(t, baseTestConfig(Config{
		RefineProposals: func(context.Context, any, *RefineProposalsRequest) (*RefineProposalsData, error) {
			handlerCalled = true
			return nil, nil
		},
	}), "refine_proposals", map[string]any{
		"idempotency_key": "idem-no-atomic-wrapper",
		"refinements":     []any{map[string]any{"proposal_id": "draft-1", "action": "finalize"}},
	})

	assert.True(t, result.IsError)
	assert.False(t, handlerCalled)
}

func callRegisteredTool(t *testing.T, cfg Config, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0.0.1"}, nil)
	Register(server, cfg)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "seller-test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer func() { _ = clientSession.Close() }()

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
