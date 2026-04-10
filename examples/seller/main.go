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

// permissiveSchema is a JSON schema that accepts any object with additional properties.
var permissiveSchema = map[string]any{"type": "object"}

// In-memory store for the seller agent.
type store struct {
	mu        sync.RWMutex
	accounts  map[string]*adcp.AccountResult
	mediaBuys map[string]*adcp.MediaBuyData
	creatives map[string]*creativeRecord
	delivery  map[string]*deliveryState
}

type creativeRecord struct {
	CreativeID string
	Name       string
	FormatID   string
	Status     string
}

type deliveryState struct {
	Impressions int
	Clicks      int
	Spend       float64
	Conversions int
}

func newStore() *store {
	return &store{
		accounts:  make(map[string]*adcp.AccountResult),
		mediaBuys: make(map[string]*adcp.MediaBuyData),
		creatives: make(map[string]*creativeRecord),
		delivery:  make(map[string]*deliveryState),
	}
}

const agentURL = "http://localhost:3001/mcp"

// Product catalog.
var products = []adcp.Product{
	{
		ProductID:    "premium-display",
		Name:         "Premium Display",
		Description:  "High-impact display placements on premium inventory",
		Channel:      "display",
		DeliveryType: "guaranteed",
		PricingOptions: []adcp.PricingOption{
			{
				PricingOptionID: "premium-display-cpm",
				PricingModel:    "cpm",
				FixedPrice:      15.00,
				Currency:        "USD",
			},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "banner-300x250"},
			{AgentURL: agentURL, ID: "banner-728x90"},
		},
	},
	{
		ProductID:    "programmatic-display",
		Name:         "Programmatic Display",
		Description:  "Auction-based display inventory across the open exchange",
		Channel:      "display",
		DeliveryType: "non_guaranteed",
		PricingOptions: []adcp.PricingOption{
			{
				PricingOptionID: "programmatic-display-floor",
				PricingModel:    "cpm",
				FloorPrice:      5.00,
				Currency:        "USD",
			},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "banner-300x250"},
			{AgentURL: agentURL, ID: "banner-160x600"},
		},
	},
	{
		ProductID:    "video-preroll",
		Name:         "Video Pre-Roll",
		Description:  "Pre-roll video ads on premium video content",
		Channel:      "olv",
		DeliveryType: "guaranteed",
		PricingOptions: []adcp.PricingOption{
			{
				PricingOptionID: "video-preroll-cpm",
				PricingModel:    "cpm",
				FixedPrice:      25.00,
				Currency:        "USD",
			},
		},
		PublisherProperties: []string{},
		FormatIDs: []adcp.FormatRef{
			{AgentURL: agentURL, ID: "video-16x9"},
		},
	},
}

// Creative formats catalog.
var creativeFormats = []adcp.CreativeFormat{
	{
		FormatID:    adcp.CreativeFormatID{AgentURL: "http://localhost:3001/mcp", ID: "banner-300x250"},
		Name:        "Medium Rectangle Banner",
		Description: "Standard 300x250 display banner",
		Renders:     []adcp.Render{{Width: 300, Height: 250}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, Description: "Banner image", AcceptedMediaTypes: []string{"image/png", "image/jpeg"}},
			{ItemType: "individual", AssetID: "click_url", AssetType: "url", Required: true, Description: "Click-through URL"},
		},
	},
	{
		FormatID:    adcp.CreativeFormatID{AgentURL: "http://localhost:3001/mcp", ID: "banner-728x90"},
		Name:        "Leaderboard Banner",
		Description: "Standard 728x90 leaderboard banner",
		Renders:     []adcp.Render{{Width: 728, Height: 90}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, Description: "Banner image", AcceptedMediaTypes: []string{"image/png", "image/jpeg"}},
			{ItemType: "individual", AssetID: "click_url", AssetType: "url", Required: true, Description: "Click-through URL"},
		},
	},
	{
		FormatID:    adcp.CreativeFormatID{AgentURL: "http://localhost:3001/mcp", ID: "banner-160x600"},
		Name:        "Wide Skyscraper",
		Description: "Standard 160x600 skyscraper banner",
		Renders:     []adcp.Render{{Width: 160, Height: 600}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, Description: "Banner image", AcceptedMediaTypes: []string{"image/png", "image/jpeg"}},
			{ItemType: "individual", AssetID: "click_url", AssetType: "url", Required: true, Description: "Click-through URL"},
		},
	},
	{
		FormatID:    adcp.CreativeFormatID{AgentURL: "http://localhost:3001/mcp", ID: "video-16x9"},
		Name:        "16:9 Video",
		Description: "Standard 16:9 video creative",
		Renders:     []adcp.Render{{Width: 1920, Height: 1080}},
		Assets: []adcp.AssetSlot{
			{ItemType: "individual", AssetID: "video", AssetType: "video", Required: true, Description: "Video file", AcceptedMediaTypes: []string{"video/mp4"}},
			{ItemType: "individual", AssetID: "click_url", AssetType: "url", Required: true, Description: "Click-through URL"},
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

func createServer(s *store) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "seller-agent",
		Version: "1.0.0",
	}, nil)

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
		ForceAccountStatus:  s.forceAccountStatus,
		ForceMediaBuyStatus: s.forceMediaBuyStatus,
		ForceCreativeStatus: s.forceCreativeStatus,
		SimulateDelivery:    s.simulateDelivery,
		SimulateBudgetSpend: s.simulateBudgetSpend,
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

			action := "created"
			if _, exists := s.accounts[accountID]; exists {
				action = "updated"
			}

			result := &adcp.AccountResult{
				AccountID: accountID,
				Brand:     brand,
				Operator:  operator,
				Action:    action,
				Status:    "active",
			}
			s.accounts[accountID] = result
			results = append(results, *result)
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

			// Parse account field if present (overrides brand/operator).
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

			// Parse governance_agents array.
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

		// Initialize delivery state for the media buy.
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
		for i, raw := range creativesRaw {
			c, _ := raw.(map[string]any)
			if c == nil {
				continue
			}

			creativeID := fmt.Sprintf("cr-%d", len(s.creatives)+i+1)
			name, _ := c["name"].(string)
			formatID, _ := c["format_id"].(string)

			s.creatives[creativeID] = &creativeRecord{
				CreativeID: creativeID,
				Name:       name,
				FormatID:   formatID,
				Status:     "approved",
			}

			results = append(results, adcp.CreativeResult{
				CreativeID: creativeID,
				Action:     "created",
				Status:     "approved",
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
				Conversions: ds.Conversions,
			}

			var byPackage []adcp.PackageDelivery
			for _, pkg := range mb.Packages {
				byPackage = append(byPackage, adcp.PackageDelivery{
					PackageID: pkg.PackageID,
					Totals:    totals,
				})
			}

			deliveries = append(deliveries, adcp.MediaBuyDelivery{
				MediaBuyID: mbID,
				Status:     mb.Status,
				Totals:     totals,
				ByPackage:  byPackage,
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

// stringVal extracts a string from a map, returning "" if missing or wrong type.
func stringVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// --- Test controller store methods ---

func (s *store) forceAccountStatus(accountID, status string) (*adcp.StateTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	acct, ok := s.accounts[accountID]
	if !ok {
		return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: fmt.Sprintf("Account %s not found", accountID)}
	}
	prev := acct.Status
	acct.Status = status
	return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
}

func (s *store) forceMediaBuyStatus(mediaBuyID, status string, rejectionReason string) (*adcp.StateTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mb, ok := s.mediaBuys[mediaBuyID]
	if !ok {
		return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: fmt.Sprintf("Media buy %s not found", mediaBuyID)}
	}
	prev := mb.Status
	mb.Status = status
	return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
}

func (s *store) forceCreativeStatus(creativeID, status string, rejectionReason string) (*adcp.StateTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cr, ok := s.creatives[creativeID]
	if !ok {
		return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: fmt.Sprintf("Creative %s not found", creativeID)}
	}
	prev := cr.Status
	cr.Status = status
	return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
}

func (s *store) simulateDelivery(mediaBuyID string, params adcp.SimulateDeliveryParams) (*adcp.SimulationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.mediaBuys[mediaBuyID]; !ok {
		return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: fmt.Sprintf("Media buy %s not found", mediaBuyID)}
	}

	ds := s.delivery[mediaBuyID]
	if ds == nil {
		ds = &deliveryState{}
		s.delivery[mediaBuyID] = ds
	}

	ds.Impressions += params.Impressions
	ds.Clicks += params.Clicks
	ds.Conversions += params.Conversions
	if params.ReportedSpend != nil {
		ds.Spend += params.ReportedSpend.Amount
	}

	return &adcp.SimulationResult{
		Success: true,
		Simulated: map[string]any{
			"impressions": params.Impressions,
			"clicks":      params.Clicks,
			"spend":       ds.Spend - float64(ds.Impressions-params.Impressions)/1000*15, // delta
			"conversions": params.Conversions,
		},
		Cumulative: map[string]any{
			"impressions": ds.Impressions,
			"clicks":      ds.Clicks,
			"spend":       ds.Spend,
			"conversions": ds.Conversions,
		},
	}, nil
}

func (s *store) simulateBudgetSpend(params adcp.SimulateBudgetParams) (*adcp.SimulationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if params.MediaBuyID != "" {
		mb, ok := s.mediaBuys[params.MediaBuyID]
		if !ok {
			return nil, &adcp.TestControllerError{Code: "NOT_FOUND", Message: fmt.Sprintf("Media buy %s not found", params.MediaBuyID)}
		}

		var totalBudget float64
		for _, pkg := range mb.Packages {
			totalBudget += pkg.Budget
		}
		spendAmount := totalBudget * (params.SpendPercentage / 100.0)

		ds := s.delivery[params.MediaBuyID]
		if ds == nil {
			ds = &deliveryState{}
			s.delivery[params.MediaBuyID] = ds
		}
		ds.Spend += spendAmount

		return &adcp.SimulationResult{
			Success: true,
			Simulated: map[string]any{
				"media_buy_id":     params.MediaBuyID,
				"spend_percentage": params.SpendPercentage,
				"spend_amount":     spendAmount,
				"total_budget":     totalBudget,
			},
			Cumulative: map[string]any{
				"total_spend":    ds.Spend,
				"total_budget":   totalBudget,
				"spent_fraction": ds.Spend / totalBudget,
			},
		}, nil
	}

	return &adcp.SimulationResult{
		Success: true,
		Simulated: map[string]any{
			"spend_percentage": params.SpendPercentage,
		},
	}, nil
}

func main() {
	s := newStore()
	if err := adcp.Serve(func() *mcp.Server { return createServer(s) }); err != nil {
		log.Fatal(err)
	}
}
