package adcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register wires AdCP tool handlers onto an MCP server. Only tools with
// registered handlers are exposed. Capabilities are auto-detected.
//
// Account resolution: if ResolveAccount is set, handlers that accept an
// AccountReference receive the resolved account. If the account is not found,
// the SDK returns ACCOUNT_NOT_FOUND automatically.
//
// Error handling: handlers can return adcp.NewError("CODE", opts) for typed
// AdCP errors. Plain errors become INTERNAL_ERROR.
//
// Usage:
//
//	adcp.Register(server, adcp.Config{
//	    ResolveAccount: func(ctx context.Context, ref adcp.AccountReference) (any, error) {
//	        return db.FindAccount(ref.Brand.Domain, ref.Operator)
//	    },
//	    GetProducts: func(ctx context.Context, acct any, req *adcp.GetProductsRequest) (*adcp.ProductsData, error) {
//	        return &adcp.ProductsData{Products: catalog.Query(req.Brief)}, nil
//	    },
//	    CreateMediaBuy: func(ctx context.Context, acct any, req *adcp.CreateMediaBuyRequest) (*adcp.MediaBuyData, error) {
//	        return oms.BookCampaign(req)
//	    },
//	})
func Register(server *mcp.Server, cfg Config) {
	protocols := detectProtocols(cfg)
	sandbox := cfg.Sandbox

	// Capabilities (always registered)
	AddTool(server, "get_adcp_capabilities", "Returns agent capabilities",
		func(ctx context.Context, req *mcp.CallToolRequest, input EmptyInput) (*mcp.CallToolResult, any, error) {
			return CapabilitiesResponse(&CapabilitiesData{
				ADCP:               &ADCPVersion{MajorVersions: []int{3}},
				SupportedProtocols: protocols,
			})
		})

	// --- Media buy tools ---

	if cfg.SyncAccounts != nil {
		AddTool(server, "sync_accounts", "Register advertiser accounts",
			func(ctx context.Context, req *mcp.CallToolRequest, input SyncAccountsRequest) (*mcp.CallToolResult, any, error) {
				results, err := cfg.SyncAccounts(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				return SyncAccountsResponse(results, sandbox)
			})
	}

	if cfg.SyncGovernance != nil {
		AddTool(server, "sync_governance", "Register governance agents",
			func(ctx context.Context, req *mcp.CallToolRequest, input SyncGovernanceRequest) (*mcp.CallToolResult, any, error) {
				results, err := cfg.SyncGovernance(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				return GovernanceResponse(results)
			})
	}

	if cfg.GetProducts != nil {
		AddTool(server, "get_products", "Available advertising products",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetProductsRequest) (*mcp.CallToolResult, any, error) {
				acct, result := resolveAccount(ctx, cfg.ResolveAccount, input.Account)
				if result != nil {
					return result, nil, nil
				}
				data, err := cfg.GetProducts(ctx, acct, &input)
				if err != nil {
					return errorToResult(err)
				}
				data.Sandbox = sandbox
				return ProductsResponse(data)
			})
	}

	if cfg.CreateMediaBuy != nil {
		AddTool(server, "create_media_buy", "Create a media buy",
			func(ctx context.Context, req *mcp.CallToolRequest, input CreateMediaBuyRequest) (*mcp.CallToolResult, any, error) {
				acct, result := resolveAccount(ctx, cfg.ResolveAccount, input.Account)
				if result != nil {
					return result, nil, nil
				}
				buy, err := cfg.CreateMediaBuy(ctx, acct, &input)
				if err != nil {
					return errorToResult(err)
				}
				return MediaBuyResponse(buy)
			})
	}

	if cfg.GetMediaBuys != nil {
		AddTool(server, "get_media_buys", "List media buys",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetMediaBuysRequest) (*mcp.CallToolResult, any, error) {
				acct, result := resolveAccount(ctx, cfg.ResolveAccount, input.Account)
				if result != nil {
					return result, nil, nil
				}
				buys, err := cfg.GetMediaBuys(ctx, acct, &input)
				if err != nil {
					return errorToResult(err)
				}
				return MediaBuysResponse(buys, sandbox)
			})
	}

	if cfg.GetDelivery != nil {
		AddTool(server, "get_media_buy_delivery", "Delivery metrics",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetMediaBuyDeliveryRequest) (*mcp.CallToolResult, any, error) {
				acct, result := resolveAccount(ctx, cfg.ResolveAccount, input.Account)
				if result != nil {
					return result, nil, nil
				}
				data, err := cfg.GetDelivery(ctx, acct, &input)
				if err != nil {
					return errorToResult(err)
				}
				return DeliveryResponse(data)
			})
	}

	// --- Creative tools ---

	if cfg.ListCreativeFormats != nil {
		AddTool(server, "list_creative_formats", "Available creative formats",
			func(ctx context.Context, req *mcp.CallToolRequest, input ListCreativeFormatsRequest) (*mcp.CallToolResult, any, error) {
				formats, err := cfg.ListCreativeFormats(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				return CreativeFormatsResponse(formats, sandbox)
			})
	}

	if cfg.SyncCreatives != nil {
		AddTool(server, "sync_creatives", "Submit creatives for review",
			func(ctx context.Context, req *mcp.CallToolRequest, input SyncCreativesRequest) (*mcp.CallToolResult, any, error) {
				results, err := cfg.SyncCreatives(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				return SyncCreativesResponse(results, sandbox)
			})
	}

	// --- Signals tools ---

	if cfg.GetSignals != nil {
		AddTool(server, "get_signals", "Discover available signals",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetSignalsRequest) (*mcp.CallToolResult, any, error) {
				signals, err := cfg.GetSignals(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				return SignalsResponse(signals, sandbox)
			})
	}

	if cfg.ActivateSignal != nil {
		AddTool(server, "activate_signal", "Activate a signal",
			func(ctx context.Context, req *mcp.CallToolRequest, input ActivateSignalRequest) (*mcp.CallToolResult, any, error) {
				deployments, err := cfg.ActivateSignal(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				return ActivateSignalResponse(deployments, sandbox)
			})
	}

	// --- Collection tools ---

	if cfg.CreateCollectionList != nil {
		AddTool(server, "create_collection_list", "Create a managed collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input CreateCollectionListRequest) (*mcp.CallToolResult, any, error) {
				result, err := cfg.CreateCollectionList(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				if result == nil || result.List == nil {
					return errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
				}
				return CreateCollectionListResponse(result.List, result.AuthToken)
			})
	}

	if cfg.GetCollectionList != nil {
		AddTool(server, "get_collection_list", "Retrieve a collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetCollectionListRequest) (*mcp.CallToolResult, any, error) {
				result, err := cfg.GetCollectionList(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				if result == nil || result.List == nil {
					return errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
				}
				return GetCollectionListResponse(result.List, result.Collections, result.Pagination)
			})
	}

	if cfg.UpdateCollectionList != nil {
		AddTool(server, "update_collection_list", "Update a collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input UpdateCollectionListRequest) (*mcp.CallToolResult, any, error) {
				list, err := cfg.UpdateCollectionList(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				if list == nil {
					return errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
				}
				return UpdateCollectionListResponse(list)
			})
	}

	if cfg.DeleteCollectionList != nil {
		AddTool(server, "delete_collection_list", "Delete a collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input DeleteCollectionListRequest) (*mcp.CallToolResult, any, error) {
				err := cfg.DeleteCollectionList(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				return DeleteCollectionListResponse(input.ListID)
			})
	}

	if cfg.ListCollectionLists != nil {
		AddTool(server, "list_collection_lists", "List collection lists",
			func(ctx context.Context, req *mcp.CallToolRequest, input ListCollectionListsRequest) (*mcp.CallToolResult, any, error) {
				result, err := cfg.ListCollectionLists(ctx, &input)
				if err != nil {
					return errorToResult(err)
				}
				if result == nil {
					return errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
				}
				return ListCollectionListsResponse(result.Lists, result.Pagination)
			})
	}
}

// Config declares which AdCP tools your agent supports. Set only the handlers
// you implement — unset handlers mean the tool isn't registered.
//
// Handlers can return adcp.NewError for typed AdCP errors, or plain errors
// (which become INTERNAL_ERROR).
type Config struct {
	// Sandbox marks all responses as sandbox/test data.
	Sandbox bool

	// ResolveAccount converts an AccountReference (brand + operator) to your
	// internal account object. Called automatically before handlers that receive
	// an account field. Return nil for unknown accounts (SDK sends ACCOUNT_NOT_FOUND).
	ResolveAccount func(ctx context.Context, ref AccountReference) (any, error)

	// --- Media buy ---
	SyncAccounts   func(ctx context.Context, req *SyncAccountsRequest) ([]AccountResult, error)
	SyncGovernance func(ctx context.Context, req *SyncGovernanceRequest) ([]GovernanceResult, error)
	GetProducts    func(ctx context.Context, acct any, req *GetProductsRequest) (*ProductsData, error)
	CreateMediaBuy func(ctx context.Context, acct any, req *CreateMediaBuyRequest) (*MediaBuyData, error)
	GetMediaBuys   func(ctx context.Context, acct any, req *GetMediaBuysRequest) ([]MediaBuyData, error)
	GetDelivery    func(ctx context.Context, acct any, req *GetMediaBuyDeliveryRequest) (*DeliveryData, error)

	// --- Creative ---
	ListCreativeFormats func(ctx context.Context, req *ListCreativeFormatsRequest) ([]CreativeFormat, error)
	SyncCreatives       func(ctx context.Context, req *SyncCreativesRequest) ([]CreativeResult, error)

	// --- Signals ---
	GetSignals     func(ctx context.Context, req *GetSignalsRequest) ([]Signal, error)
	ActivateSignal func(ctx context.Context, req *ActivateSignalRequest) ([]Deployment, error)

	// --- Collection ---
	CreateCollectionList func(ctx context.Context, req *CreateCollectionListRequest) (*CreateCollectionListResult, error)
	GetCollectionList    func(ctx context.Context, req *GetCollectionListRequest) (*GetCollectionListResult, error)
	UpdateCollectionList func(ctx context.Context, req *UpdateCollectionListRequest) (*CollectionList, error)
	DeleteCollectionList func(ctx context.Context, req *DeleteCollectionListRequest) error
	ListCollectionLists  func(ctx context.Context, req *ListCollectionListsRequest) (*ListCollectionListsResult, error)
}

// CreateCollectionListResult is the return type for Config.CreateCollectionList.
type CreateCollectionListResult struct {
	List      *CollectionList
	AuthToken string
}

// GetCollectionListResult is the return type for Config.GetCollectionList.
type GetCollectionListResult struct {
	List        *CollectionList
	Collections []ResolvedCollection
	Pagination  *PaginationResponse
}

// ListCollectionListsResult is the return type for Config.ListCollectionLists.
type ListCollectionListsResult struct {
	Lists      []CollectionList
	Pagination *PaginationResponse
}

func resolveAccount(ctx context.Context, resolver func(context.Context, AccountReference) (any, error), ref any) (any, *mcp.CallToolResult) {
	if resolver == nil {
		return nil, nil
	}
	var acctRef AccountReference
	switch v := ref.(type) {
	case AccountReference:
		acctRef = v
	case *AccountReference:
		if v == nil {
			return nil, nil
		}
		acctRef = *v
	default:
		// Account field present but not a recognized type — likely a schema change
		result, _, _ := Errorf("INTERNAL_ERROR", ErrorOptions{Message: "unexpected account reference type"})
		return nil, result
	}
	if acctRef.Brand == nil && acctRef.Operator == "" {
		return nil, nil
	}
	acct, err := resolver(ctx, acctRef)
	if err != nil {
		result, _, _ := errorToResult(err)
		return nil, result
	}
	if acct == nil {
		result, _, _ := Errorf("ACCOUNT_NOT_FOUND", ErrorOptions{
			Message:    "Account not found. Call sync_accounts first to register this brand and operator.",
			Field:      "account",
			Suggestion: "Call sync_accounts with the brand and operator before making this request.",
		})
		return nil, result
	}
	return acct, nil
}

func detectProtocols(cfg Config) []string {
	var protocols []string
	if cfg.GetProducts != nil || cfg.CreateMediaBuy != nil {
		protocols = append(protocols, "media_buy")
	}
	if cfg.GetSignals != nil || cfg.ActivateSignal != nil {
		protocols = append(protocols, "signals")
	}
	if cfg.CreateCollectionList != nil || cfg.GetCollectionList != nil {
		protocols = append(protocols, "collection")
	}
	return protocols
}
