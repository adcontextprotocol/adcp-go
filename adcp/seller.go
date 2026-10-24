package adcp

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register wires AdCP tool handlers onto an MCP server. Only tools with
// registered handlers are exposed. supported_protocols is auto-detected.
//
// IdempotencyReplayTTL is required (see Config docs). Register panics at
// startup if it is unset or out of range — AdCP 3.0 requires sellers to
// declare adcp.idempotency.replay_ttl_seconds in capabilities.
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
//	    IdempotencyReplayTTL: 24 * time.Hour,
//	    ResolveAccount: func(ctx context.Context, ref adcp.AccountReference) (any, error) {
//	        // The SDK validates the reference before this call (see
//	        // Config.ResolveAccount): exactly one form is set — AccountID,
//	        // or Brand+Operator. Brand is nil for an account_id reference —
//	        // branch first. Check the caller may act for the account
//	        // (adcp.PrincipalFromContext(ctx)) and honor ref.Sandbox.
//	        if ref.AccountID != "" {
//	            return db.FindAccountByID(ref.AccountID)
//	        }
//	        return db.FindAccount(ref.Brand.Domain, ref.Operator)
//	    },
//	    GetProducts: func(ctx context.Context, acct any, req *adcp.GetProductsRequest) (*adcp.ProductsData, error) {
//	        return &adcp.ProductsData{Products: catalog.Query(req.Brief)}, nil
//	    },
//	    CreateMediaBuy: func(ctx context.Context, acct any, req *adcp.CreateMediaBuyRequest) (adcp.CreateMediaBuyResponse, error) {
//	        return oms.BookCampaign(req)
//	    },
//	})
func Register(server *mcp.Server, cfg Config) {
	caps := buildCapabilities(cfg)
	sandbox := cfg.Sandbox

	// Capabilities (always registered)
	AddTool(server, "get_adcp_capabilities", "Returns agent capabilities",
		func(ctx context.Context, req *mcp.CallToolRequest, input GetAdcpCapabilitiesRequest) (*mcp.CallToolResult, any, error) {
			data, ok := capabilitiesForVersion(caps, input.AdcpVersion, input.AdcpMajorVersion)
			if !ok {
				result, out, err := Errorf("VERSION_UNSUPPORTED", ErrorOptions{
					Message:    "unsupported AdCP version",
					Suggestion: "Call get_adcp_capabilities without a version pin to discover supported_versions.",
					Details:    map[string]any{"supported_versions": caps.ADCP.SupportedVersions, "supported_majors": caps.ADCP.MajorVersions},
				})
				return attachContext(result, input.Context), out, err
			}
			data, ok = filterCapabilitiesByProtocols(data, input.Protocols)
			if !ok {
				result, out, err := Errorf("UNSUPPORTED_FEATURE", ErrorOptions{
					Message: "unsupported AdCP protocol",
					Field:   "protocols",
					Details: map[string]any{
						"requested_protocols": input.Protocols,
						"supported_protocols": caps.SupportedProtocols,
					},
				})
				return attachContext(result, input.Context), out, err
			}
			result, out, err := CapabilitiesResponse(data)
			return attachContext(result, input.Context), out, err
		})

	// --- Media buy tools ---

	if cfg.SyncAccounts != nil {
		AddTool(server, "sync_accounts", "Register advertiser accounts",
			func(ctx context.Context, req *mcp.CallToolRequest, input SyncAccountsRequest) (*mcp.CallToolResult, any, error) {
				results, err := cfg.SyncAccounts(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				result, out, err := SyncAccountsResponse(results, sandbox)
				return attachContext(result, input.Context), out, err
			})
	}

	if cfg.SyncGovernance != nil {
		AddTool(server, "sync_governance", "Register governance agents",
			func(ctx context.Context, req *mcp.CallToolRequest, input SyncGovernanceRequest) (*mcp.CallToolResult, any, error) {
				results, err := cfg.SyncGovernance(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				result, out, err := GovernanceResponse(results)
				return attachContext(result, input.Context), out, err
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
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				data.Sandbox = sandbox
				data.Context = input.Context
				result, out, err := ProductsResponse(data)
				return attachContext(result, input.Context), out, err
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
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				buy = stampCreateMediaBuyResult(buy, sandbox, input.Context)
				result, out, err := MediaBuyResponse(buy)
				return attachContext(result, input.Context), out, err
			})
	}

	if cfg.GetMediaBuys != nil {
		AddTool(server, "get_media_buys", "List media buys",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetMediaBuysRequest) (*mcp.CallToolResult, any, error) {
				acct, result := resolveAccount(ctx, cfg.ResolveAccount, input.Account)
				if result != nil {
					return result, nil, nil
				}
				data, err := cfg.GetMediaBuys(ctx, acct, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				if data == nil {
					result, out, e := errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
					return attachContext(result, input.Context), out, e
				}
				if data.Sandbox == nil {
					data.Sandbox = Bool(sandbox)
				}
				data.Context = input.Context
				result, out, err := MediaBuysDataResponse(data)
				return attachContext(result, input.Context), out, err
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
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				data.Context = input.Context
				result, out, err := DeliveryResponse(data)
				return attachContext(result, input.Context), out, err
			})
	}

	// --- Creative tools ---

	if cfg.ListCreativeFormats != nil {
		AddTool(server, "list_creative_formats", "Available creative formats",
			func(ctx context.Context, req *mcp.CallToolRequest, input ListCreativeFormatsRequest) (*mcp.CallToolResult, any, error) {
				formats, err := cfg.ListCreativeFormats(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				result, out, err := CreativeFormatsResponse(formats, sandbox)
				return attachContext(result, input.Context), out, err
			})
	}

	if cfg.SyncCreatives != nil {
		AddTool(server, "sync_creatives", "Submit creatives for review",
			func(ctx context.Context, req *mcp.CallToolRequest, input SyncCreativesRequest) (*mcp.CallToolResult, any, error) {
				results, err := cfg.SyncCreatives(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				result, out, err := SyncCreativesResponse(results, sandbox)
				return attachContext(result, input.Context), out, err
			})
	}

	// --- Signals tools ---

	if cfg.GetSignals != nil {
		AddTool(server, "get_signals", "Discover available signals",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetSignalsRequest) (*mcp.CallToolResult, any, error) {
				signals, err := cfg.GetSignals(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				result, out, err := SignalsResponse(signals, sandbox)
				return attachContext(result, input.Context), out, err
			})
	}

	if cfg.ActivateSignal != nil {
		AddTool(server, "activate_signal", "Activate a signal",
			func(ctx context.Context, req *mcp.CallToolRequest, input ActivateSignalRequest) (*mcp.CallToolResult, any, error) {
				deployments, err := cfg.ActivateSignal(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				result, out, err := ActivateSignalResponse(deployments, sandbox)
				return attachContext(result, input.Context), out, err
			})
	}

	// --- Collection tools ---

	if cfg.CreateCollectionList != nil {
		AddTool(server, "create_collection_list", "Create a managed collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input CreateCollectionListRequest) (*mcp.CallToolResult, any, error) {
				result, err := cfg.CreateCollectionList(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				if result == nil || result.List == nil {
					result, out, e := errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
					return attachContext(result, input.Context), out, e
				}
				callResult, out, err := CreateCollectionListResponse(result.List, result.AuthToken)
				return attachContext(callResult, input.Context), out, err
			})
	}

	if cfg.GetCollectionList != nil {
		AddTool(server, "get_collection_list", "Retrieve a collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input GetCollectionListRequest) (*mcp.CallToolResult, any, error) {
				result, err := cfg.GetCollectionList(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				if result == nil || result.List == nil {
					result, out, e := errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
					return attachContext(result, input.Context), out, e
				}
				callResult, out, err := GetCollectionListResponse(result.List, result.Collections, result.Pagination)
				return attachContext(callResult, input.Context), out, err
			})
	}

	if cfg.UpdateCollectionList != nil {
		AddTool(server, "update_collection_list", "Update a collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input UpdateCollectionListRequest) (*mcp.CallToolResult, any, error) {
				list, err := cfg.UpdateCollectionList(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				if list == nil {
					result, out, e := errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
					return attachContext(result, input.Context), out, e
				}
				result, out, err := UpdateCollectionListResponse(list)
				return attachContext(result, input.Context), out, err
			})
	}

	if cfg.DeleteCollectionList != nil {
		AddTool(server, "delete_collection_list", "Delete a collection list",
			func(ctx context.Context, req *mcp.CallToolRequest, input DeleteCollectionListRequest) (*mcp.CallToolResult, any, error) {
				err := cfg.DeleteCollectionList(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				result, out, err := DeleteCollectionListResponse(input.ListID)
				return attachContext(result, input.Context), out, err
			})
	}

	if cfg.ListCollectionLists != nil {
		AddTool(server, "list_collection_lists", "List collection lists",
			func(ctx context.Context, req *mcp.CallToolRequest, input ListCollectionListsRequest) (*mcp.CallToolResult, any, error) {
				result, err := cfg.ListCollectionLists(ctx, &input)
				if err != nil {
					result, out, e := errorToResult(err)
					return attachContext(result, input.Context), out, e
				}
				if result == nil {
					result, out, e := errorToResult(NewError("INTERNAL_ERROR", ErrorOptions{Message: "handler returned nil result"}))
					return attachContext(result, input.Context), out, e
				}
				callResult, out, err := ListCollectionListsResponse(result.Lists, result.Pagination)
				return attachContext(callResult, input.Context), out, err
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

	// IdempotencyReplayTTL is how long this agent retains a canonical response
	// for an idempotency_key. Required by AdCP 3.0 — sellers MUST declare their
	// replay window. Must be in [1h, 7d]; 24h is recommended. Register panics
	// if this is zero or outside the valid range.
	IdempotencyReplayTTL time.Duration

	// Capabilities, if set, declares the full typed capabilities response.
	// supported_protocols and adcp.idempotency are filled in automatically if
	// left empty. Use this to declare account / media_buy / signals / etc.
	// blocks. If nil, a minimal response with just adcp + supported_protocols
	// is built from the registered handlers.
	Capabilities *CapabilitiesData

	// ResolveAccount converts an AccountReference (brand + operator, or a bare
	// account_id) to your internal account object. Called automatically before
	// handlers that receive an account field. Return nil for unknown accounts
	// (SDK sends ACCOUNT_NOT_FOUND).
	//
	// The SDK validates a supplied reference against the pinned
	// core/account-ref.json schema before invoking the resolver, so exactly
	// one form is set on the reference the resolver receives: AccountID, or
	// Brand and Operator together. Brand is nil for an account_id reference,
	// so resolvers must branch on AccountID before dereferencing Brand. A
	// supplied reference that matches neither schema form (for example an
	// operator without a brand) is rejected with INVALID_REQUEST and never
	// reaches the resolver. An omitted account field — including a decoded
	// reference carrying no identifier — keeps the no-account path and does
	// not invoke the resolver either. Sandbox is a modifier the flattened Go
	// type can carry with either form; it is passed through for the resolver
	// to honor, not treated as an identifier.
	ResolveAccount func(ctx context.Context, ref AccountReference) (any, error)

	// --- Media buy ---
	SyncAccounts   func(ctx context.Context, req *SyncAccountsRequest) ([]AccountResult, error)
	SyncGovernance func(ctx context.Context, req *SyncGovernanceRequest) ([]GovernanceResult, error)
	GetProducts    func(ctx context.Context, acct any, req *GetProductsRequest) (*ProductsData, error)
	CreateMediaBuy func(ctx context.Context, acct any, req *CreateMediaBuyRequest) (CreateMediaBuyResponse, error)
	GetMediaBuys   func(ctx context.Context, acct any, req *GetMediaBuysRequest) (*GetMediaBuysResponse, error)
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

// accountDomainPattern is the domain pattern the pinned schemas apply to
// both core/account-ref.json's operator and core/brand-ref.json's domain.
var accountDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// validAccountReference reports whether a supplied (identifier-bearing)
// AccountReference matches one of the two forms in the pinned
// core/account-ref.json schema: {account_id} alone, or the natural key
// {brand, operator} with a schema-valid brand domain and operator. It adds
// no rules of its own beyond the schema. Sandbox is deliberately not part
// of the check: the flattened Go type carries it as a modifier on either
// form, and the resolver — not the SDK — applies its semantics. References
// carrying no identifier at all never reach this function; resolveAccount
// keeps them on the omitted-account path.
func validAccountReference(ref AccountReference) bool {
	if ref.AccountID != "" {
		// account_id form: no natural-key fields may accompany it.
		return ref.Brand == nil && ref.Operator == ""
	}
	// Natural-key form: brand and operator are both required, and both
	// domains must match the schema pattern (brand-ref requires domain).
	if ref.Brand == nil || ref.Operator == "" {
		return false
	}
	return accountDomainPattern.MatchString(ref.Brand.Domain) &&
		accountDomainPattern.MatchString(ref.Operator)
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
	// An account_id-only reference still names an account: it must go to the
	// resolver so an unknown ID surfaces ACCOUNT_NOT_FOUND instead of
	// silently running with no account (public pricing).
	if acctRef.Brand == nil && acctRef.Operator == "" && acctRef.AccountID == "" {
		return nil, nil
	}
	// A supplied reference must match one of the pinned schema's two forms
	// before it reaches the resolver: an operator-only reference would
	// otherwise arrive with a nil Brand and crash resolvers that dereference
	// it. See validAccountReference.
	if !validAccountReference(acctRef) {
		result, _, _ := Errorf("INVALID_REQUEST", ErrorOptions{
			Message:    "Invalid account reference.",
			Field:      "account",
			Suggestion: "Send either an account_id, or a brand and operator together.",
		})
		return nil, result
	}
	acct, err := resolver(ctx, acctRef)
	if err != nil {
		result, _, _ := errorToResult(err)
		return nil, result
	}
	if acct == nil {
		msg := "Account not found. Call sync_accounts first to register this brand and operator."
		suggestion := "Call sync_accounts with the brand and operator before making this request."
		if acctRef.AccountID != "" {
			// Per enums/error-code.json: for an account_id, the fix is to
			// verify the ID, not to re-register a brand/operator pair.
			msg = "Account not found."
			suggestion = "Verify the account_id via list_accounts, or contact the seller."
		}
		result, _, _ := Errorf("ACCOUNT_NOT_FOUND", ErrorOptions{
			Message:    msg,
			Field:      "account",
			Suggestion: suggestion,
		})
		return nil, result
	}
	return acct, nil
}

func stampCreateMediaBuyResult(result CreateMediaBuyResponse, sandbox bool, context any) CreateMediaBuyResponse {
	switch v := result.(type) {
	case *CreateMediaBuySuccess:
		if v.Sandbox == nil {
			v.Sandbox = Bool(sandbox)
		}
		v.Context = context
		return v
	case CreateMediaBuySuccess:
		if v.Sandbox == nil {
			v.Sandbox = Bool(sandbox)
		}
		v.Context = context
		return v
	case *CreateMediaBuySubmitted:
		v.Context = context
		return v
	case CreateMediaBuySubmitted:
		v.Context = context
		return v
	case *CreateMediaBuyError:
		v.Context = context
		return v
	case CreateMediaBuyError:
		v.Context = context
		return v
	}
	return result
}

// detectProtocols returns supported_protocols values inferred from the
// handlers the caller wired up. Only protocols in the 3.0 schema enum
// (media_buy, signals, governance, sponsored_intelligence, creative, brand)
// may be emitted. Collection tools are part of a 3.x extension, not a
// supported_protocols value, so collection handlers do not affect this list.
func detectProtocols(cfg Config) []string {
	var protocols []string
	if cfg.SyncAccounts != nil || cfg.GetProducts != nil || cfg.CreateMediaBuy != nil || cfg.GetMediaBuys != nil || cfg.GetDelivery != nil {
		protocols = append(protocols, "media_buy")
	}
	if cfg.SyncGovernance != nil {
		protocols = append(protocols, "governance")
	}
	if cfg.ListCreativeFormats != nil || cfg.SyncCreatives != nil {
		protocols = append(protocols, "creative")
	}
	if cfg.GetSignals != nil || cfg.ActivateSignal != nil {
		protocols = append(protocols, "signals")
	}
	return protocols
}

// idempotencyReplayTTLMin and Max mirror the 3.0 schema bounds on
// adcp.idempotency.replay_ttl_seconds.
const (
	idempotencyReplayTTLMin = 1 * time.Hour
	idempotencyReplayTTLMax = 7 * 24 * time.Hour
)

// buildCapabilities constructs the CapabilitiesData returned from
// get_adcp_capabilities. It panics if IdempotencyReplayTTL is missing or
// out of range, if the caller and Config disagree on the replay window, or if
// no supported_protocols can be determined — all are startup-time config bugs
// that fail the 3.0 capabilities schema.
func buildCapabilities(cfg Config) *CapabilitiesData {
	ttl := cfg.IdempotencyReplayTTL
	if ttl < idempotencyReplayTTLMin || ttl > idempotencyReplayTTLMax {
		panic(fmt.Sprintf("adcp.Register: Config.IdempotencyReplayTTL must be 1h–7d (got %s) — set it to 24*time.Hour; AdCP 3.0 requires sellers to declare adcp.idempotency.replay_ttl_seconds", ttl))
	}
	ttlSeconds := int(ttl.Seconds())

	var caps CapabilitiesData
	if cfg.Capabilities != nil {
		caps = *cfg.Capabilities
	}
	if caps.ADCP == nil {
		caps.ADCP = &ADCPVersion{MajorVersions: []int{3}}
	}
	if len(caps.ADCP.MajorVersions) == 0 {
		caps.ADCP.MajorVersions = []int{3}
	}
	if len(caps.ADCP.SupportedVersions) == 0 {
		caps.ADCP.SupportedVersions = SupportedADCPVersions()
	}
	if caps.AdcpVersion == "" {
		if version, ok := NegotiateADCPVersion("", 0, caps.ADCP.SupportedVersions); ok {
			caps.AdcpVersion = version
		}
	}
	if caps.AdcpMajorVersion == 0 {
		if major, ok := MajorFromADCPVersion(caps.AdcpVersion); ok {
			caps.AdcpMajorVersion = major
		}
	}
	if existing := caps.ADCP.Idempotency.ReplayTTLSeconds; existing != 0 && existing != ttlSeconds {
		panic(fmt.Sprintf("adcp.Register: Config.IdempotencyReplayTTL (%ds) conflicts with Capabilities.ADCP.Idempotency.ReplayTTLSeconds (%ds) — set one or the other, not both", ttlSeconds, existing))
	}
	caps.ADCP.Idempotency = IdempotencyCaps{Supported: true, ReplayTTLSeconds: ttlSeconds}

	if len(caps.SupportedProtocols) == 0 {
		caps.SupportedProtocols = detectProtocols(cfg)
	}
	if len(caps.SupportedProtocols) == 0 {
		panic("adcp.Register: no supported_protocols — wire at least one handler (e.g. GetProducts) or set Capabilities.SupportedProtocols; AdCP 3.0 requires minItems: 1")
	}
	return &caps
}

func capabilitiesForVersion(base *CapabilitiesData, requestedVersion string, requestedMajor int) (*CapabilitiesData, bool) {
	if base == nil || base.ADCP == nil {
		return nil, false
	}
	servedVersion, ok := NegotiateADCPVersion(requestedVersion, requestedMajor, base.ADCP.SupportedVersions)
	if !ok {
		return nil, false
	}
	major, ok := MajorFromADCPVersion(servedVersion)
	if !ok {
		return nil, false
	}

	caps := *base
	adcpCaps := *base.ADCP
	adcpCaps.MajorVersions = append([]int(nil), base.ADCP.MajorVersions...)
	adcpCaps.SupportedVersions = append([]string(nil), base.ADCP.SupportedVersions...)
	caps.ADCP = &adcpCaps
	caps.SupportedProtocols = append([]string(nil), base.SupportedProtocols...)
	caps.AdcpVersion = servedVersion
	caps.AdcpMajorVersion = major
	return &caps, true
}

func filterCapabilitiesByProtocols(base *CapabilitiesData, protocols []string) (*CapabilitiesData, bool) {
	if base == nil || len(protocols) == 0 {
		return base, true
	}

	requested := make(map[string]bool, len(protocols))
	for _, protocol := range protocols {
		requested[protocol] = true
	}

	filtered := *base
	filtered.SupportedProtocols = make([]string, 0, len(base.SupportedProtocols))
	for _, protocol := range base.SupportedProtocols {
		if requested[protocol] {
			filtered.SupportedProtocols = append(filtered.SupportedProtocols, protocol)
		}
	}
	if len(filtered.SupportedProtocols) == 0 {
		return nil, false
	}

	filtered.Account = nil
	filtered.MediaBuy = nil
	filtered.Signals = nil
	filtered.Governance = nil
	filtered.SponsoredIntelligence = nil
	filtered.Brand = nil
	filtered.Creative = nil
	filtered.Measurement = nil
	filtered.WholesaleFeedVersioning = nil
	filtered.WholesaleFeedWebhooks = nil

	for _, protocol := range filtered.SupportedProtocols {
		switch protocol {
		case "media_buy":
			filtered.Account = base.Account
			filtered.MediaBuy = base.MediaBuy
			filtered.WholesaleFeedVersioning = base.WholesaleFeedVersioning
			filtered.WholesaleFeedWebhooks = base.WholesaleFeedWebhooks
		case "signals":
			filtered.Signals = base.Signals
			filtered.WholesaleFeedVersioning = base.WholesaleFeedVersioning
			filtered.WholesaleFeedWebhooks = base.WholesaleFeedWebhooks
		case "governance":
			filtered.Governance = base.Governance
		case "sponsored_intelligence":
			filtered.SponsoredIntelligence = base.SponsoredIntelligence
		case "brand":
			filtered.Brand = base.Brand
		case "creative":
			filtered.Creative = base.Creative
		case "measurement":
			filtered.Measurement = base.Measurement
		}
	}

	return &filtered, true
}
