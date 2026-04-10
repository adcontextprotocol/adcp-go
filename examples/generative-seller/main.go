package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var permissiveSchema = map[string]any{"type": "object"}

const agentURL = "http://localhost:3001/mcp"

type store struct {
	mu        sync.RWMutex
	accounts  map[string]string // accountID -> status
	mediaBuys map[string]*adcp.MediaBuyData
	creatives map[string]string // creativeID -> status
	delivery  map[string]*deliveryState
}

type deliveryState struct {
	Impressions int
	Clicks      int
	Spend       float64
	Conversions int
}

func newStore() *store {
	return &store{
		accounts:  make(map[string]string),
		mediaBuys: make(map[string]*adcp.MediaBuyData),
		creatives: make(map[string]string),
		delivery:  make(map[string]*deliveryState),
	}
}

var products = []adcp.Product{
	{
		ProductID:    "ai-display",
		Name:         "AI-Generated Display",
		Description:  "AI-generated display ads from creative briefs",
		Channel:      "display",
		DeliveryType: "non_guaranteed",
		PricingOptions: []adcp.PricingOption{
			{PricingOptionID: "ai-display-floor", PricingModel: "cpm", FloorPrice: 8.00, Currency: "USD"},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "display_300x250_generative"},
			{AgentURL: agentURL, ID: "display_300x250"},
		},
	},
	{
		ProductID:    "ai-video",
		Name:         "AI-Generated Video",
		Description:  "AI-generated video ads from creative briefs",
		Channel:      "olv",
		DeliveryType: "guaranteed",
		PricingOptions: []adcp.PricingOption{
			{PricingOptionID: "ai-video-cpm", PricingModel: "cpm", FixedPrice: 30.00, Currency: "USD"},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "video_30s_generative"},
			{AgentURL: agentURL, ID: "video_30s"},
		},
	},
}

var creativeFormats = []adcp.CreativeFormat{
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_300x250_generative"},
		Name:     "Generated Display 300x250",
		Renders:  []adcp.Render{{Width: 300, Height: 250}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "brief", AssetType: "brief", Required: true, Description: "Creative brief with messaging and brand guidelines"},
		},
	},
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_300x250"},
		Name:     "Display 300x250",
		Renders:  []adcp.Render{{Width: 300, Height: 250}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}},
		},
	},
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "video_30s_generative"},
		Name:     "Generated Video 30s",
		Renders:  []adcp.Render{{Width: 1920, Height: 1080}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "brief", AssetType: "brief", Required: true, Description: "Video creative brief"},
		},
	},
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "video_30s"},
		Name:     "Video 30s Pre-Roll",
		Renders:  []adcp.Render{{Width: 1920, Height: 1080}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "video", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}},
		},
	},
}

// makeResult builds a CallToolResult with both text content and structured data.
func makeResult(data any, summary string) (*mcp.CallToolResult, error) {
	b, _ := json.Marshal(data)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: summary + "\n" + string(b)}},
		StructuredContent: data,
	}, nil
}

// makeErrorResult builds an error CallToolResult with ADCP error structure.
func makeErrorResult(code string, message string) (*mcp.CallToolResult, error) {
	errData := map[string]any{"adcp_error": map[string]any{"code": code, "message": message, "recovery": "terminal"}}
	b, _ := json.Marshal(errData)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		IsError:           true,
		StructuredContent: errData,
	}, nil
}

// parseArgs unmarshals raw tool arguments into a map.
func parseArgs(req *mcp.CallToolRequest) map[string]any {
	args := make(map[string]any)
	if req.Params.Arguments != nil {
		_ = json.Unmarshal(req.Params.Arguments, &args)
	}
	return args
}

// stringVal extracts a string from a map, returning "" if missing or wrong type.
func stringVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func createServer(s *store) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "generative-seller-agent", Version: "1.0.0"}, nil)

	registerGetCapabilities(server)
	registerSyncAccounts(server, s)
	registerSyncGovernance(server)
	registerGetProducts(server)
	registerCreateMediaBuy(server, s)
	registerGetMediaBuys(server, s)
	registerListCreativeFormats(server)
	registerSyncCreatives(server, s)
	registerGetMediaBuyDelivery(server, s)

	adcp.RegisterTestController(server, &adcp.TestControllerStore{
		ForceAccountStatus: func(id, status string) (*adcp.StateTransition, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			prev, ok := s.accounts[id]
			if !ok {
				return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: "Account not found"}
			}
			s.accounts[id] = status
			return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
		},
		ForceMediaBuyStatus: func(id, status, reason string) (*adcp.StateTransition, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			mb, ok := s.mediaBuys[id]
			if !ok {
				return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: "Media buy not found"}
			}
			prev := mb.Status
			mb.Status = status
			return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
		},
		ForceCreativeStatus: func(id, status, reason string) (*adcp.StateTransition, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			prev, ok := s.creatives[id]
			if !ok {
				return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: "Creative not found"}
			}
			s.creatives[id] = status
			return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
		},
		SimulateDelivery: func(id string, p adcp.SimulateDeliveryParams) (*adcp.SimulationResult, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.mediaBuys[id]; !ok {
				return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: "Media buy not found"}
			}
			ds := s.delivery[id]
			if ds == nil {
				ds = &deliveryState{}
				s.delivery[id] = ds
			}
			ds.Impressions += p.Impressions
			ds.Clicks += p.Clicks
			if p.ReportedSpend != nil {
				ds.Spend += p.ReportedSpend.Amount
			}
			return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"impressions": p.Impressions}}, nil
		},
		SimulateBudgetSpend: func(p adcp.SimulateBudgetParams) (*adcp.SimulationResult, error) {
			return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"spend_percentage": p.SpendPercentage}}, nil
		},
	})

	return server
}

// --- get_adcp_capabilities ---

func registerGetCapabilities(server *mcp.Server) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "get_adcp_capabilities",
		Description: "Returns the AdCP capabilities supported by this seller agent.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		data := &adcp.CapabilitiesData{
			SupportedProtocols: []string{"media_buy", "compliance_testing"},
		}
		return makeResult(data, "Agent capabilities retrieved")
	})
}

// --- sync_accounts ---

func registerSyncAccounts(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "sync_accounts",
		Description: "Registers or updates advertiser accounts with this seller.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)

		accountsRaw, _ := args["accounts"].([]any)
		if len(accountsRaw) == 0 {
			return makeErrorResult("INVALID_INPUT", "accounts array is required")
		}

		s.mu.Lock()
		defer s.mu.Unlock()

		var results []adcp.AccountResult
		for i, raw := range accountsRaw {
			acct, _ := raw.(map[string]any)
			if acct == nil {
				continue
			}

			var brand *adcp.BrandReference
			if brandMap, ok := acct["brand"].(map[string]any); ok {
				brand = &adcp.BrandReference{
					Domain: stringVal(brandMap, "domain"),
				}
			}

			domain := ""
			if brand != nil {
				domain = brand.Domain
			}
			accountID := fmt.Sprintf("acct-%s-%d", domain, i+1)

			operator, _ := acct["operator"].(string)

			s.accounts[accountID] = "active"

			results = append(results, adcp.AccountResult{
				AccountID: accountID,
				Brand:     brand,
				Operator:  operator,
				Action:    "created",
				Status:    "active",
			})
		}

		out := map[string]any{
			"accounts": results,
			"sandbox":  true,
		}
		return makeResult(out, fmt.Sprintf("Synced %d accounts", len(results)))
	})
}

// --- sync_governance ---

func registerSyncGovernance(server *mcp.Server) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "sync_governance",
		Description: "Registers governance agents for advertiser accounts.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)

		accountsRaw, _ := args["accounts"].([]any)

		var results []adcp.GovernanceResult
		for _, raw := range accountsRaw {
			acctMap, _ := raw.(map[string]any)
			if acctMap == nil {
				continue
			}

			var brand *adcp.BrandReference
			if brandMap, ok := acctMap["brand"].(map[string]any); ok {
				brand = &adcp.BrandReference{
					Domain: stringVal(brandMap, "domain"),
				}
			}

			operator, _ := acctMap["operator"].(string)
			govAcct := &adcp.GovernanceAccount{Brand: brand, Operator: operator}

			if acctObj, ok := acctMap["account"].(map[string]any); ok {
				if b, ok := acctObj["brand"].(map[string]any); ok {
					govAcct.Brand = &adcp.BrandReference{
						Domain: stringVal(b, "domain"),
					}
				}
				if op, ok := acctObj["operator"].(string); ok {
					govAcct.Operator = op
				}
			}

			var agents []adcp.GovernanceAgent
			if agentsRaw, ok := acctMap["governance_agents"].([]any); ok {
				for _, a := range agentsRaw {
					if agentMap, ok := a.(map[string]any); ok {
						var categories []string
						if cats, ok := agentMap["categories"].([]any); ok {
							for _, c := range cats {
								if s, ok := c.(string); ok {
									categories = append(categories, s)
								}
							}
						}
						agents = append(agents, adcp.GovernanceAgent{
							URL:        stringVal(agentMap, "url"),
							Categories: categories,
						})
					}
				}
			}

			results = append(results, adcp.GovernanceResult{
				Account:          govAcct,
				Status:           "synced",
				GovernanceAgents: agents,
			})
		}

		out := map[string]any{
			"accounts": results,
		}
		return makeResult(out, "Governance synced")
	})
}

// --- get_products ---

func registerGetProducts(server *mcp.Server) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "get_products",
		Description: "Returns available advertising products from this seller.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		data := &adcp.ProductsData{
			Products: products,
			Sandbox:  true,
		}
		return makeResult(data, fmt.Sprintf("Found %d products", len(products)))
	})
}

// --- create_media_buy ---

func registerCreateMediaBuy(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "create_media_buy",
		Description: "Creates a media buy order with one or more packages.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)

		s.mu.Lock()
		defer s.mu.Unlock()

		mediaBuyID := fmt.Sprintf("mb-%d", len(s.mediaBuys)+1)
		currency := stringVal(args, "currency")
		if currency == "" {
			currency = "USD"
		}

		packagesRaw, _ := args["packages"].([]any)
		var pkgs []adcp.Package
		for i, raw := range packagesRaw {
			p, _ := raw.(map[string]any)
			if p == nil {
				continue
			}
			budget, _ := p["budget"].(float64)
			pkgs = append(pkgs, adcp.Package{
				PackageID:       fmt.Sprintf("%s-pkg-%d", mediaBuyID, i+1),
				ProductID:       stringVal(p, "product_id"),
				PricingOptionID: stringVal(p, "pricing_option_id"),
				Budget:          budget,
				Status:          "active",
			})
		}

		data := &adcp.MediaBuyData{
			MediaBuyID: mediaBuyID,
			Status:     "active",
			Currency:   currency,
			Packages:   pkgs,
		}
		s.mediaBuys[mediaBuyID] = data
		s.delivery[mediaBuyID] = &deliveryState{}

		return makeResult(data, fmt.Sprintf("Media buy %s created", mediaBuyID))
	})
}

// --- get_media_buys ---

func registerGetMediaBuys(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "get_media_buys",
		Description: "Returns all media buys for the seller.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		var buys []adcp.MediaBuyData
		for _, mb := range s.mediaBuys {
			buys = append(buys, *mb)
		}

		out := map[string]any{
			"media_buys": buys,
			"sandbox":    true,
		}
		return makeResult(out, fmt.Sprintf("Found %d media buys", len(buys)))
	})
}

// --- list_creative_formats ---

func registerListCreativeFormats(server *mcp.Server) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "list_creative_formats",
		Description: "Returns available creative formats supported by this seller.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out := map[string]any{
			"formats": creativeFormats,
			"sandbox": true,
		}
		return makeResult(out, fmt.Sprintf("Found %d creative formats", len(creativeFormats)))
	})
}

// --- sync_creatives ---

func registerSyncCreatives(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "sync_creatives",
		Description: "Submits creatives for review and approval.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)

		creativesRaw, _ := args["creatives"].([]any)

		s.mu.Lock()
		defer s.mu.Unlock()

		var results []adcp.CreativeResult
		for _, raw := range creativesRaw {
			c, _ := raw.(map[string]any)
			if c == nil {
				continue
			}

			creativeID, _ := c["creative_id"].(string)
			formatID := fmt.Sprintf("%v", c["format_id"])

			status := "accepted"
			if strings.Contains(formatID, "generative") {
				status = "pending_review"
			}

			s.creatives[creativeID] = status

			results = append(results, adcp.CreativeResult{
				CreativeID: creativeID,
				Action:     "created",
				Status:     status,
			})
		}

		out := map[string]any{
			"creatives": results,
			"results":   results,
			"sandbox":   true,
		}
		return makeResult(out, fmt.Sprintf("Synced %d creatives", len(results)))
	})
}

// --- get_media_buy_delivery ---

func registerGetMediaBuyDelivery(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "get_media_buy_delivery",
		Description: "Returns delivery metrics for media buys.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)
		mediaBuyID, _ := args["media_buy_id"].(string)

		s.mu.RLock()
		defer s.mu.RUnlock()

		now := time.Now().UTC()
		period := adcp.ReportingPeriod{
			Start: now.AddDate(0, 0, -7).Format(time.RFC3339),
			End:   now.Format(time.RFC3339),
		}

		deliveries := make([]adcp.MediaBuyDelivery, 0)

		for mbID, mb := range s.mediaBuys {
			if mediaBuyID != "" && mbID != mediaBuyID {
				continue
			}

			ds := s.delivery[mbID]
			if ds == nil {
				ds = &deliveryState{}
			}

			totals := adcp.DeliveryTotals{
				Impressions: ds.Impressions,
				Clicks:      ds.Clicks,
				Spend:       ds.Spend,
			}

			deliveries = append(deliveries, adcp.MediaBuyDelivery{
				MediaBuyID: mbID,
				Status:     mb.Status,
				Totals:     totals,
				ByPackage:  []adcp.PackageDelivery{},
			})
		}

		data := map[string]any{
			"reporting_period":     period,
			"media_buy_deliveries": deliveries,
			"media_buys":          deliveries,
		}
		return makeResult(data, fmt.Sprintf("Delivery data for %d media buys", len(deliveries)))
	})
}

func main() {
	s := newStore()
	if err := adcp.Serve(func() *mcp.Server { return createServer(s) }); err != nil {
		log.Fatal(err)
	}
}
