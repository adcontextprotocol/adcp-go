// Command hello-seller is a minimal AdCP seller agent: the smallest thing
// that actually implements the protocol, not a fully-worked reference. If
// you're building a real seller, start here and swap the pieces marked
// "SWAP:" for your own ad server / OMS integration — reference/seller-agent
// (in this repo) is the fully-worked version once you need more of the
// protocol surface (test controller, forced states, signals, collections,
// governance, etc.).
//
// Core protocol surface (this file): account resolution, capabilities,
// get_products, create_media_buy, get_media_buys.
// Extension surface (extensions.go): update_media_buy, sync_creatives,
// list_creatives, get_media_buy_delivery — wire these when your backend
// supports them; each is independent of the others.
//
// State is an in-memory map. It resets every time the process restarts —
// see the README's fork checklist before using any of this in production.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// agentURL is this agent's own public URL, used as the FormatRef.AgentURL for
// formats it defines. SWAP: set ADCP_AGENT_URL to your real public URL in
// production — buyers dereference this to resolve your creative formats.
func agentURL() string {
	if u := os.Getenv("ADCP_AGENT_URL"); u != "" {
		return u
	}
	port := 3001
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	return fmt.Sprintf("http://localhost:%d/mcp", port)
}

// backend holds all seller state. SWAP: replace the in-memory maps with
// your real ad server / OMS / database calls — every method below is the
// integration point for exactly one piece of that.
type backend struct {
	mu        sync.Mutex
	accounts  map[string]*adcp.AccountResult
	products  map[string]*adcp.Product
	mediaBuys map[string]*adcp.MediaBuyData
	creatives map[string]*adcp.CreativeListItem
	nextBuyID int
}

func newBackend() *backend {
	url := agentURL()
	product := adcp.Product{
		ProductID:           "premium-display",
		Name:                "Premium Display",
		Description:         "High-impact display placements across our premium publisher network.",
		PublisherProperties: []adcp.PublisherPropertySelector{{PublisherDomain: "example.com", SelectionType: "all"}},
		Channels:            []string{"display"},
		FormatIDs:           []adcp.FormatRef{{AgentURL: url, ID: "display_300x250"}},
		DeliveryType:        "guaranteed",
		PricingOptions:      []adcp.PricingOption{{PricingOptionID: "pd-cpm-15", PricingModel: "cpm", FixedPrice: adcp.Ptr(15.00), Currency: "USD"}},
		ReportingCapabilities: adcp.ReportingCapabilities{
			AvailableReportingFrequencies: []string{"daily"},
			ExpectedDelayMinutes:          60,
			Timezone:                      "UTC",
			AvailableMetrics:              []string{"impressions", "spend"},
			DateRangeSupport:              "date_range",
		},
	}
	return &backend{
		accounts:  make(map[string]*adcp.AccountResult),
		products:  map[string]*adcp.Product{product.ProductID: &product},
		mediaBuys: make(map[string]*adcp.MediaBuyData),
		creatives: make(map[string]*adcp.CreativeListItem),
	}
}

// resolveAccount converts an AccountReference (brand + operator) into your
// internal account object. It's called automatically before every handler
// that receives an account field — you never call it yourself.
//
// SWAP: look up the real account in your CRM/billing system, and return
// (nil, nil) — not an error — for a genuinely unknown account; the SDK turns
// that into the correct ACCOUNT_NOT_FOUND response for you.
func (b *backend) resolveAccount(_ context.Context, ref adcp.AccountReference) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	domain := ""
	if ref.Brand != nil {
		domain = ref.Brand.Domain
	}
	id := fmt.Sprintf("acct-%s-%s", domain, ref.Operator)
	if acct, ok := b.accounts[id]; ok {
		return acct, nil
	}
	// Demo-only: silently mint a new account for anything unrecognized.
	// SWAP: a real implementation returns (nil, nil) here instead, so the
	// buyer gets ACCOUNT_NOT_FOUND and calls sync_accounts first.
	acct := &adcp.AccountResult{AccountID: id, Brand: ref.Brand, Operator: ref.Operator, Action: "existing", Status: "active"}
	b.accounts[id] = acct
	return acct, nil
}

// getProducts returns the seller's catalog for get_products. SWAP: query
// your ad server / OMS for real, request-specific inventory instead of the
// fixed catalog below.
func (b *backend) getProducts(_ context.Context, _ any, _ *adcp.GetProductsRequest) (*adcp.ProductsData, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	products := make([]adcp.Product, 0, len(b.products))
	for _, p := range b.products {
		products = append(products, *p)
	}
	return &adcp.ProductsData{Products: products, CacheScope: "public"}, nil
}

// createMediaBuy books a new media buy. SWAP: this is where you'd call your
// ad server's real booking API and persist the result durably — this demo
// only appends to an in-memory map, which is lost on restart.
func (b *backend) createMediaBuy(_ context.Context, _ any, input *adcp.CreateMediaBuyRequest) (adcp.CreateMediaBuyResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextBuyID++
	id := fmt.Sprintf("mb-%d", b.nextBuyID)

	pkgs := make([]adcp.PackageStatus, 0, len(input.Packages))
	var totalBudget float64
	for i, p := range input.Packages {
		pkgs = append(pkgs, adcp.PackageStatus{Package: adcp.Package{
			PackageID:       fmt.Sprintf("%s-pkg-%d", id, i+1),
			ProductID:       p.ProductID,
			PricingOptionID: p.PricingOptionID,
			Budget:          p.Budget,
			StartTime:       p.StartTime,
			EndTime:         p.EndTime,
		}})
		totalBudget += p.Budget
	}

	now := time.Now().UTC().Format(time.RFC3339)
	buy := &adcp.MediaBuyData{
		MediaBuyID:   id,
		Status:       "active",
		Currency:     "USD",
		TotalBudget:  totalBudget,
		Packages:     pkgs,
		ConfirmedAt:  now,
		CreatedAt:    now,
		UpdatedAt:    now,
		Revision:     1,
		ValidActions: []string{adcp.MediaBuyValidActionPause, adcp.MediaBuyValidActionCancel},
		Context:      input.Context,
	}
	b.mediaBuys[id] = buy

	packages := make([]adcp.Package, 0, len(pkgs))
	for _, s := range pkgs {
		packages = append(packages, s.Package)
	}
	return &adcp.CreateMediaBuySuccess{
		MediaBuyID:     buy.MediaBuyID,
		MediaBuyStatus: buy.Status,
		ConfirmedAt:    adcp.Ptr(buy.ConfirmedAt),
		Revision:       buy.Revision,
		Currency:       buy.Currency,
		TotalBudget:    buy.TotalBudget,
		ValidActions:   buy.ValidActions,
		Packages:       packages,
		// SWAP: derive Sandbox from your actual environment/config, not a
		// hardcoded literal — see extensions.go's top comment for the same
		// point on update_media_buy's response.
		Sandbox: adcp.Bool(true),
		Context: buy.Context,
	}, nil
}

// getMediaBuys implements get_media_buys — a read of whatever createMediaBuy
// persisted. SWAP: read from your durable store instead of the in-memory map.
func (b *backend) getMediaBuys(_ context.Context, _ any, input *adcp.GetMediaBuysRequest) (*adcp.GetMediaBuysResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	buys := make([]adcp.MediaBuyData, 0, len(b.mediaBuys))
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
	return &adcp.GetMediaBuysResponse{MediaBuys: buys}, nil
}

func newServer(b *backend) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "hello-seller", Version: "0.1.0"}, nil)

	cfg := adcp.Config{
		Sandbox: true,
		// Required by AdCP 3.0 — Register panics if this is zero. 24h is the
		// recommended default; widen it if your replay window needs longer.
		IdempotencyReplayTTL: 24 * time.Hour,
		Capabilities: &adcp.CapabilitiesData{
			Account:  &adcp.AccountCapabilities{SupportedBilling: []string{"operator"}, Sandbox: adcp.Bool(true)},
			MediaBuy: &adcp.MediaBuyCapabilities{SupportedPricingModels: []string{"cpm"}},
			Creative: &adcp.CreativeCapabilities{HasCreativeLibrary: adcp.Bool(true)},
		},
		ResolveAccount: b.resolveAccount,
		GetProducts:    b.getProducts,
		CreateMediaBuy: b.createMediaBuy,
		GetMediaBuys:   b.getMediaBuys,
	}
	addExtensionHandlers(&cfg, b)

	adcp.Register(server, cfg)
	wireExtensionTools(server, b)

	return server
}

func main() {
	b := newBackend()
	log.Fatal(adcp.Serve(func() *mcp.Server {
		return newServer(b)
	}))
}
