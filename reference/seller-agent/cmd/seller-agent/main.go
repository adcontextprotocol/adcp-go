// Reference seller agent using adcp.Register with handler functions.
// Each handler represents where you'd integrate your real ad server / OMS.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const agentURL = "http://localhost:3001/mcp"

// --- Your product catalog and creative formats ---

var products = []adcp.Product{
	{
		ProductID: "premium-display", Name: "Premium Display",
		Description:  "High-impact display placements across our premium publisher network.",
		Channels:     []string{"display"}, DeliveryType: "guaranteed",
		PricingOptions: []adcp.PricingOption{{PricingOptionID: "pd-cpm-15", PricingModel: "cpm", FixedPrice: 15.00, Currency: "USD"}},
		FormatIDs:      []adcp.FormatRef{{AgentURL: agentURL, ID: "banner-300x250"}, {AgentURL: agentURL, ID: "banner-728x90"}},
	},
	{
		ProductID: "video-preroll", Name: "Video Pre-Roll",
		Description: "15 and 30 second pre-roll video ads.",
		Channels:    []string{"video"}, DeliveryType: "non_guaranteed",
		PricingOptions: []adcp.PricingOption{
			{PricingOptionID: "vp-cpm-25", PricingModel: "cpm", FixedPrice: 25.00, Currency: "USD"},
			{PricingOptionID: "vp-cpcv-05", PricingModel: "cpcv", FixedPrice: 0.05, Currency: "USD"},
		},
		FormatIDs: []adcp.FormatRef{{AgentURL: agentURL, ID: "video-15s"}, {AgentURL: agentURL, ID: "video-30s"}},
	},
}

var formats = []adcp.CreativeFormat{
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "banner-300x250"}, Name: "Medium Rectangle", Renders: []adcp.Render{{Width: 300, Height: 250}}, Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/png", "image/jpeg"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "banner-728x90"}, Name: "Leaderboard", Renders: []adcp.Render{{Width: 728, Height: 90}}, Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/png", "image/jpeg"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "video-15s"}, Name: "Video :15", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "video_file", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "video-30s"}, Name: "Video :30", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "video_file", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}}}},
}

// --- Your backend state (replace with your real DB / ad server client) ---

type backend struct {
	mu        sync.RWMutex
	accounts  map[string]*adcp.AccountResult
	mediaBuys map[string]*adcp.MediaBuyData
	creatives map[string]string
	delivery  map[string]*deliveryState
	buySeq    atomic.Int64
}

type deliveryState struct{ Impressions, Clicks int; Spend float64 }

func main() {
	b := &backend{
		accounts:  make(map[string]*adcp.AccountResult),
		mediaBuys: make(map[string]*adcp.MediaBuyData),
		creatives: make(map[string]string),
		delivery:  make(map[string]*deliveryState),
	}

	log.Fatal(adcp.Serve(func() *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "reference-seller", Version: "1.0.0"}, nil)

		adcp.Register(server, adcp.Config{
			Sandbox:              true,
			IdempotencyReplayTTL: 24 * time.Hour,
			ResolveAccount: func(_ context.Context, ref adcp.AccountReference) (any, error) {
				b.mu.RLock()
				defer b.mu.RUnlock()
				domain := ""
				if ref.Brand != nil {
					domain = ref.Brand.Domain
				}
				id := fmt.Sprintf("acct-%s-%s", domain, ref.Operator)
				if acct, ok := b.accounts[id]; ok {
					return acct, nil
				}
				return nil, nil
			},
			SyncAccounts: func(_ context.Context, input *adcp.SyncAccountsRequest) ([]adcp.AccountResult, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				results := make([]adcp.AccountResult, 0, len(input.Accounts))
				for _, acct := range input.Accounts {
					domain := "unknown"
					if acct.Brand != nil {
						domain = acct.Brand.Domain
					}
					id := fmt.Sprintf("acct-%s-%s", domain, acct.Operator)
					result := adcp.AccountResult{AccountID: id, Brand: acct.Brand, Operator: acct.Operator, Action: "created", Status: "active"}
					if existing, ok := b.accounts[id]; ok {
						result.Action = "updated"
						result.Status = existing.Status
					}
					b.accounts[id] = &result
					results = append(results, result)
				}
				return results, nil
			},
			SyncGovernance: func(_ context.Context, input *adcp.SyncGovernanceRequest) ([]adcp.GovernanceResult, error) {
				results := make([]adcp.GovernanceResult, 0, len(input.Accounts))
				for _, acct := range input.Accounts {
					govAcct := acct.Account
					if govAcct == nil {
						govAcct = &adcp.GovernanceAccount{Brand: acct.Brand, Operator: acct.Operator}
					}
					results = append(results, adcp.GovernanceResult{Account: govAcct, Status: "synced", GovernanceAgents: acct.GovernanceAgents})
				}
				return results, nil
			},
			GetProducts: func(_ context.Context, _ any, _ *adcp.GetProductsRequest) (*adcp.ProductsData, error) {
				// In production: query your inventory system
				return &adcp.ProductsData{Products: products}, nil
			},
			CreateMediaBuy: func(_ context.Context, _ any, input *adcp.CreateMediaBuyRequest) (*adcp.MediaBuyData, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				// In production: book into your OMS / ad server
				n := b.buySeq.Add(1)
				id := fmt.Sprintf("mb-%d", n)
				pkgs := make([]adcp.Package, 0, len(input.Packages))
				for i, p := range input.Packages {
					pkgs = append(pkgs, adcp.Package{
						PackageID: fmt.Sprintf("%s-pkg-%d", id, i+1), ProductID: p.ProductID,
						PricingOptionID: p.PricingOptionID, Budget: p.Budget,
						StartTime: p.StartTime, EndTime: p.EndTime,
						AgencyEstimateNumber: p.AgencyEstimateNumber,
						MeasurementTerms: p.MeasurementTerms, PerformanceStandards: p.PerformanceStandards,
					})
				}
				var totalBudget float64
				for _, p := range input.Packages {
					totalBudget += p.Budget
				}
				buy := &adcp.MediaBuyData{MediaBuyID: id, Status: "active", TotalBudget: totalBudget, Packages: pkgs}
				b.mediaBuys[id] = buy
				for _, pkg := range pkgs {
					b.delivery[pkg.PackageID] = &deliveryState{}
				}
				return buy, nil
			},
			GetMediaBuys: func(_ context.Context, _ any, input *adcp.GetMediaBuysRequest) ([]adcp.MediaBuyData, error) {
				b.mu.RLock()
				defer b.mu.RUnlock()
				buys := make([]adcp.MediaBuyData, 0)
				if len(input.MediaBuyIDs) > 0 {
					for _, id := range input.MediaBuyIDs {
						if buy, ok := b.mediaBuys[id]; ok {
							buys = append(buys, *buy)
						}
					}
				} else {
					for _, buy := range b.mediaBuys {
						buys = append(buys, *buy)
					}
				}
				return buys, nil
			},
			ListCreativeFormats: func(_ context.Context, _ *adcp.ListCreativeFormatsRequest) ([]adcp.CreativeFormat, error) {
				return formats, nil
			},
			SyncCreatives: func(_ context.Context, input *adcp.SyncCreativesRequest) ([]adcp.CreativeResult, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				// In production: ingest into your trafficking system
				results := make([]adcp.CreativeResult, 0, len(input.Creatives))
				for _, c := range input.Creatives {
					action := "created"
					if _, exists := b.creatives[c.CreativeID]; exists {
						action = "updated"
					}
					b.creatives[c.CreativeID] = "approved"
					results = append(results, adcp.CreativeResult{CreativeID: c.CreativeID, Action: action, Status: "approved"})
				}
				return results, nil
			},
			GetDelivery: func(_ context.Context, _ any, input *adcp.GetMediaBuyDeliveryRequest) (*adcp.DeliveryData, error) {
				b.mu.RLock()
				defer b.mu.RUnlock()
				// In production: pull from your reporting system
				now := time.Now().UTC()
				ids := input.MediaBuyIDs
				if len(ids) == 0 {
					for id := range b.mediaBuys {
						ids = append(ids, id)
					}
				}
				deliveries := make([]adcp.MediaBuyDelivery, 0, len(ids))
				for _, mbID := range ids {
					buy, ok := b.mediaBuys[mbID]
					if !ok {
						continue
					}
					pkgDel := make([]adcp.PackageDelivery, 0)
					var totImps, totClicks int
					var totSpend float64
					for _, pkg := range buy.Packages {
						ds := b.delivery[pkg.PackageID]
						if ds == nil {
							ds = &deliveryState{}
						}
						pkgDel = append(pkgDel, adcp.PackageDelivery{PackageID: pkg.PackageID, Totals: adcp.DeliveryTotals{Impressions: float64(ds.Impressions), Clicks: float64(ds.Clicks), Spend: ds.Spend}})
						totImps += ds.Impressions
						totClicks += ds.Clicks
						totSpend += ds.Spend
					}
					deliveries = append(deliveries, adcp.MediaBuyDelivery{MediaBuyID: mbID, Status: buy.Status, Totals: adcp.DeliveryTotals{Impressions: float64(totImps), Clicks: float64(totClicks), Spend: totSpend}, ByPackage: pkgDel})
				}
				return &adcp.DeliveryData{ReportingPeriod: adcp.ReportingPeriod{Start: now.Add(-24 * time.Hour).Format(time.RFC3339), End: now.Format(time.RFC3339)}, MediaBuyDeliveries: deliveries}, nil
			},
		})

		// Test controller — sandbox only. Do not register in production.
		if os.Getenv("ADCP_SANDBOX") != "false" {
		adcp.RegisterTestController(server, &adcp.TestControllerStore{
			ForceAccountStatus: func(accountID, status string) (*adcp.StateTransition, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				acct, ok := b.accounts[accountID]
				if !ok {
					return nil, fmt.Errorf("NOT_FOUND")
				}
				prev := acct.Status
				acct.Status = status
				return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
			},
			ForceMediaBuyStatus: func(mediaBuyID, status, reason string) (*adcp.StateTransition, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				buy, ok := b.mediaBuys[mediaBuyID]
				if !ok {
					return nil, fmt.Errorf("NOT_FOUND")
				}
				prev := buy.Status
				if prev == "completed" || prev == "rejected" || prev == "canceled" {
					return nil, fmt.Errorf("INVALID_TRANSITION")
				}
				buy.Status = status
				return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
			},
			ForceCreativeStatus: func(creativeID, status, reason string) (*adcp.StateTransition, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				prev, ok := b.creatives[creativeID]
				if !ok {
					return nil, fmt.Errorf("NOT_FOUND")
				}
				b.creatives[creativeID] = status
				return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
			},
			SimulateDelivery: func(mediaBuyID string, p adcp.SimulateDeliveryParams) (*adcp.SimulationResult, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				buy, ok := b.mediaBuys[mediaBuyID]
				if !ok {
					return nil, fmt.Errorf("NOT_FOUND")
				}
				var spend float64
				if p.ReportedSpend != nil {
					spend = p.ReportedSpend.Amount
				}
				for _, pkg := range buy.Packages {
					ds := b.delivery[pkg.PackageID]
					if ds == nil {
						ds = &deliveryState{}
						b.delivery[pkg.PackageID] = ds
					}
					ds.Impressions += p.Impressions
					ds.Clicks += p.Clicks
					ds.Spend += spend
				}
				return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"impressions": p.Impressions, "clicks": p.Clicks, "spend": spend}}, nil
			},
			SimulateBudgetSpend: func(p adcp.SimulateBudgetParams) (*adcp.SimulationResult, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				buy, ok := b.mediaBuys[p.MediaBuyID]
				if !ok {
					return nil, fmt.Errorf("NOT_FOUND")
				}
				var total float64
				for _, pkg := range buy.Packages {
					total += pkg.Budget
				}
				spend := total * p.SpendPercentage
				for _, pkg := range buy.Packages {
					if total == 0 {
						continue
					}
					ds := b.delivery[pkg.PackageID]
					if ds == nil {
						ds = &deliveryState{}
						b.delivery[pkg.PackageID] = ds
					}
					ds.Spend += spend * (pkg.Budget / total)
				}
				return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"spend": spend, "percentage": p.SpendPercentage}}, nil
			},
		})
		}

		return server
	}))
}
