package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var permissiveSchema = map[string]any{"type": "object"}

const agentURL = "http://localhost:3001/mcp"

type store struct {
	mu           sync.RWMutex
	accounts     map[string]string
	mediaBuys    map[string]*adcp.MediaBuyData
	creatives    map[string]string
	delivery     map[string]*deliveryState
	catalogs     map[string]int // catalogID -> itemCount
	eventSources map[string]bool
	eventsLogged int
}

type deliveryState struct {
	Impressions int
	Clicks      int
	Spend       float64
	Conversions int
}

func newStore() *store {
	return &store{
		accounts:     make(map[string]string),
		mediaBuys:    make(map[string]*adcp.MediaBuyData),
		creatives:    make(map[string]string),
		delivery:     make(map[string]*deliveryState),
		catalogs:     make(map[string]int),
		eventSources: make(map[string]bool),
	}
}

var products = []adcp.Product{
	{
		ProductID:    "sponsored-product",
		Name:         "Sponsored Product",
		Description:  "Promoted product listings in search results",
		Channel:      "retail_media",
		DeliveryType: "non_guaranteed",
		PricingOptions: []adcp.PricingOption{
			{PricingOptionID: "sp-cpc", PricingModel: "cpc", FixedPrice: 0.50, Currency: "USD"},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "product-card"},
		},
	},
	{
		ProductID:    "homepage-banner",
		Name:         "Homepage Banner",
		Description:  "Premium banner placement on homepage",
		Channel:      "display",
		DeliveryType: "guaranteed",
		PricingOptions: []adcp.PricingOption{
			{PricingOptionID: "hb-cpm", PricingModel: "cpm", FixedPrice: 20.00, Currency: "USD"},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "banner-728x90"},
		},
	},
	{
		ProductID:    "search-placement",
		Name:         "Search Placement",
		Description:  "Promoted placement in search results",
		Channel:      "retail_media",
		DeliveryType: "non_guaranteed",
		PricingOptions: []adcp.PricingOption{
			{PricingOptionID: "sp2-cpc", PricingModel: "cpc", FixedPrice: 0.75, Currency: "USD"},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "product-card"},
		},
	},
}

var creativeFormats = []adcp.CreativeFormat{
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "product-card"},
		Name:     "Product Card",
		Renders:  []adcp.Render{{Width: 300, Height: 250}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}},
		},
	},
	{
		FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "banner-728x90"},
		Name:     "Leaderboard",
		Renders:  []adcp.Render{{Width: 728, Height: 90}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}},
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
	server := mcp.NewServer(&mcp.Implementation{Name: "retail-media-agent", Version: "1.0.0"}, nil)

	registerGetCapabilities(server)
	registerSyncAccounts(server, s)
	registerSyncGovernance(server)
	registerGetProducts(server)
	registerCreateMediaBuy(server, s)
	registerGetMediaBuys(server, s)
	registerListCreativeFormats(server)
	registerSyncCreatives(server, s)
	registerGetMediaBuyDelivery(server, s)
	registerSyncCatalogs(server, s)
	registerSyncEventSources(server, s)
	registerLogEvent(server, s)
	registerProvidePerformanceFeedback(server)

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

			s.creatives[creativeID] = "accepted"

			results = append(results, adcp.CreativeResult{
				CreativeID: creativeID,
				Action:     "created",
				Status:     "accepted",
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

// --- sync_catalogs ---

func registerSyncCatalogs(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "sync_catalogs",
		Description: "Accept product catalog feeds.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)

		catalogsRaw, _ := args["catalogs"].([]any)

		s.mu.Lock()
		defer s.mu.Unlock()

		var results []adcp.CatalogResult
		for _, raw := range catalogsRaw {
			c, _ := raw.(map[string]any)
			if c == nil {
				continue
			}

			catalogID := stringVal(c, "catalog_id")
			items, _ := c["items"].([]any)
			count := len(items)
			if count == 0 {
				count = 10 // default mock
			}

			s.catalogs[catalogID] = count

			results = append(results, adcp.CatalogResult{
				CatalogID:     catalogID,
				Action:        "created",
				ItemCount:     count,
				ItemsApproved: count,
			})
		}

		out := map[string]any{
			"catalogs": results,
			"sandbox":  true,
		}
		return makeResult(out, fmt.Sprintf("Synced %d catalogs", len(results)))
	})
}

// --- sync_event_sources ---

func registerSyncEventSources(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "sync_event_sources",
		Description: "Register event tracking sources.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)

		sourcesRaw, _ := args["event_sources"].([]any)

		s.mu.Lock()
		defer s.mu.Unlock()

		var results []adcp.EventSourceResult
		for _, raw := range sourcesRaw {
			es, _ := raw.(map[string]any)
			if es == nil {
				continue
			}

			eventSourceID := stringVal(es, "event_source_id")
			s.eventSources[eventSourceID] = true

			results = append(results, adcp.EventSourceResult{
				EventSourceID: eventSourceID,
				Action:        "created",
			})
		}

		out := map[string]any{
			"event_sources": results,
			"sandbox":       true,
		}
		return makeResult(out, fmt.Sprintf("Synced %d event sources", len(results)))
	})
}

// --- log_event ---

func registerLogEvent(server *mcp.Server, s *store) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "log_event",
		Description: "Accept conversion events.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := parseArgs(req)

		eventsRaw, _ := args["events"].([]any)
		count := len(eventsRaw)

		s.mu.Lock()
		s.eventsLogged += count
		s.mu.Unlock()

		out := map[string]any{
			"events_received":  count,
			"events_processed": count,
			"sandbox":          true,
		}
		return makeResult(out, fmt.Sprintf("Logged %d events", count))
	})
}

// --- provide_performance_feedback ---

func registerProvidePerformanceFeedback(server *mcp.Server) {
	server.AddTool(&mcp.Tool{InputSchema: permissiveSchema,
		Name:        "provide_performance_feedback",
		Description: "Accept performance metrics.",
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out := map[string]any{
			"success": true,
			"sandbox": true,
		}
		return makeResult(out, "Feedback received")
	})
}

func main() {
	s := newStore()
	if err := adcp.Serve(func() *mcp.Server { return createServer(s) }); err != nil {
		log.Fatal(err)
	}
}
