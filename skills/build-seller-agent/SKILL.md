---
name: build-seller-agent
description: Use when building an AdCP seller agent in Go — a publisher, SSP, or retail media network that sells advertising inventory to buyer agents.
---

# Build a Seller Agent (Go)

## Overview

A seller agent receives briefs from buyers, returns products with pricing, accepts media buys, manages creatives, and reports delivery.

## When to Use

- User wants to build an agent that sells ad inventory in Go
- User mentions publisher, SSP, retail media, or media network in the context of AdCP

**Not this skill:** buyer/DSP agents, audience signals (`skills/build-signals-agent/`), creative rendering (`skills/build-creative-agent/`)

## Before Writing Code

Ask the user — don't guess.

1. **What kind of seller?** Premium publisher (guaranteed, fixed pricing) / SSP (non-guaranteed, auction) / Retail media (both)
2. **Guaranteed or non-guaranteed?** `delivery_type: "guaranteed"` vs `"non_guaranteed"`. Many sellers support both.
3. **Products and pricing.** Each product needs: product_id, name, description, channel, delivery_type, pricing_options, publisher_properties (empty array OK), format_ids.
4. **Approval workflow.** Instant (`status: "active"`) or async (`status: "pending_approval"`). Async can surface via buyer polling `get_media_buys` OR via signed webhooks to `push_notification_config.url` — see `skills/build-webhook-publisher/` for the emission pattern. Webhooks are baseline in AdCP 3.0; polling is the legacy fallback.
5. **Creative management.** Standard (`list_creative_formats` + `sync_creatives`) or none.

## Complete Skeleton

`adcp.Register` wires handler functions to the server. Set only the handlers you support — capabilities are auto-detected. Account resolution and error formatting are automatic.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "sync"
    "sync/atomic"
    "time"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

const agentURL = "http://localhost:3001/mcp"

// --- Your backend (replace with your real DB / ad server client) ---

type backend struct {
    mu        sync.RWMutex
    accounts  map[string]*adcp.AccountResult
    mediaBuys map[string]*adcp.MediaBuyData
    creatives map[string]string
    delivery  map[string]*struct{ Impressions, Clicks int; Spend float64 }
    buySeq    atomic.Int64
}

// --- Products and formats (your catalog) ---

var products = []adcp.Product{
    {
        ProductID: "premium-display", Name: "Premium Display",
        Description: "High-impact display placements.",
        Channel: "display", DeliveryType: "guaranteed",
        PricingOptions: []adcp.PricingOption{
            {PricingOptionID: "pd-cpm", PricingModel: "cpm", FixedPrice: 15.00, Currency: "USD"},
        },
        PublisherProperties: []string{},
        FormatIDs: []adcp.FormatRef{{AgentURL: agentURL, ID: "banner-300x250"}},
    },
}

var formats = []adcp.CreativeFormat{
    {
        FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "banner-300x250"},
        Name:     "Medium Rectangle",
        Renders:  []adcp.Render{{Width: 300, Height: 250}},
        Assets: []adcp.AssetSlot{
            {ItemType: "individual", AssetID: "image", AssetType: "image", Required: true,
                AcceptedMediaTypes: []string{"image/png", "image/jpeg"}},
        },
    },
}

func main() {
    b := &backend{
        accounts:  make(map[string]*adcp.AccountResult),
        mediaBuys: make(map[string]*adcp.MediaBuyData),
        creatives: make(map[string]string),
        delivery:  make(map[string]*struct{ Impressions, Clicks int; Spend float64 }),
    }

    log.Fatal(adcp.Serve(func() *mcp.Server {
        server := mcp.NewServer(&mcp.Implementation{Name: "my-seller", Version: "1.0.0"}, nil)

        adcp.Register(server, adcp.Config{
            Sandbox:              true,
            IdempotencyReplayTTL: 24 * time.Hour, // required — how long you retain idempotency_key responses

            // Optional — declare typed 3.0 capability blocks. Omit to ship a minimal response.
            Capabilities: &adcp.CapabilitiesData{
                Account: &adcp.AccountCapabilities{SupportedBilling: []string{"agent"}},
                MediaBuy: &adcp.MediaBuyCapabilities{
                    SupportedPricingModels: []string{"cpm"},
                    Portfolio:              &adcp.PortfolioCaps{PublisherDomains: []string{"example.com"}},
                },
            },

            ResolveAccount: func(_ context.Context, ref adcp.AccountReference) (any, error) {
                b.mu.RLock()
                defer b.mu.RUnlock()
                domain := ""
                if ref.Brand != nil { domain = ref.Brand.Domain }
                id := fmt.Sprintf("acct-%s-%s", domain, ref.Operator)
                if acct, ok := b.accounts[id]; ok { return acct, nil }
                return nil, nil
            },

            SyncAccounts: func(_ context.Context, req *adcp.SyncAccountsRequest) ([]adcp.AccountResult, error) {
                b.mu.Lock()
                defer b.mu.Unlock()
                results := make([]adcp.AccountResult, 0, len(req.Accounts))
                for _, acct := range req.Accounts {
                    domain := "unknown"
                    if acct.Brand != nil { domain = acct.Brand.Domain }
                    id := fmt.Sprintf("acct-%s-%s", domain, acct.Operator)
                    result := adcp.AccountResult{AccountID: id, Brand: acct.Brand, Operator: acct.Operator, Action: "created", Status: "active"}
                    if existing, ok := b.accounts[id]; ok { result.Action = "updated"; result.Status = existing.Status }
                    b.accounts[id] = &result
                    results = append(results, result)
                }
                return results, nil
            },

            SyncGovernance: func(_ context.Context, req *adcp.SyncGovernanceRequest) ([]adcp.GovernanceResult, error) {
                results := make([]adcp.GovernanceResult, 0, len(req.Accounts))
                for _, acct := range req.Accounts {
                    govAcct := acct.Account
                    if govAcct == nil { govAcct = &adcp.GovernanceAccount{Brand: acct.Brand, Operator: acct.Operator} }
                    results = append(results, adcp.GovernanceResult{Account: govAcct, Status: "synced", GovernanceAgents: acct.GovernanceAgents})
                }
                return results, nil
            },

            GetProducts: func(_ context.Context, _ any, _ *adcp.GetProductsRequest) (*adcp.ProductsData, error) {
                // In production: query your inventory, apply brief matching
                return &adcp.ProductsData{Products: products}, nil
            },

            CreateMediaBuy: func(_ context.Context, _ any, req *adcp.CreateMediaBuyRequest) (*adcp.MediaBuyData, error) {
                b.mu.Lock()
                defer b.mu.Unlock()
                // In production: book into your OMS / ad server
                n := b.buySeq.Add(1)
                id := fmt.Sprintf("mb-%d", n)
                pkgs := make([]adcp.Package, 0, len(req.Packages))
                for i, p := range req.Packages {
                    pkgs = append(pkgs, adcp.Package{
                        PackageID: fmt.Sprintf("%s-pkg-%d", id, i+1), ProductID: p.ProductID,
                        PricingOptionID: p.PricingOptionID, Budget: p.Budget,
                        StartTime: p.StartTime, EndTime: p.EndTime,
                        AgencyEstimateNumber: p.AgencyEstimateNumber,
                        MeasurementTerms: p.MeasurementTerms, PerformanceStandards: p.PerformanceStandards,
                    })
                }
                buy := &adcp.MediaBuyData{MediaBuyID: id, Status: "active", Currency: "USD", Packages: pkgs}
                b.mediaBuys[id] = buy
                for _, pkg := range pkgs { b.delivery[pkg.PackageID] = &struct{ Impressions, Clicks int; Spend float64 }{} }
                return buy, nil
            },

            GetMediaBuys: func(_ context.Context, _ any, req *adcp.GetMediaBuysRequest) ([]adcp.MediaBuyData, error) {
                b.mu.RLock()
                defer b.mu.RUnlock()
                buys := make([]adcp.MediaBuyData, 0)
                if len(req.MediaBuyIds) > 0 {
                    for _, id := range req.MediaBuyIds {
                        if buy, ok := b.mediaBuys[id]; ok { buys = append(buys, *buy) }
                    }
                } else {
                    for _, buy := range b.mediaBuys { buys = append(buys, *buy) }
                }
                return buys, nil
            },

            ListCreativeFormats: func(_ context.Context, _ *adcp.ListCreativeFormatsRequest) ([]adcp.CreativeFormat, error) {
                return formats, nil
            },

            SyncCreatives: func(_ context.Context, req *adcp.SyncCreativesRequest) ([]adcp.CreativeResult, error) {
                b.mu.Lock()
                defer b.mu.Unlock()
                // In production: ingest into your trafficking system
                results := make([]adcp.CreativeResult, 0, len(req.Creatives))
                for _, c := range req.Creatives {
                    action := "created"
                    if _, exists := b.creatives[c.CreativeID]; exists { action = "updated" }
                    b.creatives[c.CreativeID] = "approved"
                    results = append(results, adcp.CreativeResult{CreativeID: c.CreativeID, Action: action, Status: "approved"})
                }
                return results, nil
            },

            GetDelivery: func(_ context.Context, _ any, req *adcp.GetMediaBuyDeliveryRequest) (*adcp.DeliveryData, error) {
                b.mu.RLock()
                defer b.mu.RUnlock()
                // In production: pull from your reporting system
                now := time.Now().UTC()
                ids := req.MediaBuyIds
                if len(ids) == 0 { for id := range b.mediaBuys { ids = append(ids, id) } }
                deliveries := make([]adcp.MediaBuyDelivery, 0)
                for _, mbID := range ids {
                    buy, ok := b.mediaBuys[mbID]
                    if !ok { continue }
                    pkgDel := make([]adcp.PackageDelivery, 0)
                    for _, pkg := range buy.Packages {
                        pkgDel = append(pkgDel, adcp.PackageDelivery{PackageID: pkg.PackageID, Totals: adcp.DeliveryTotals{}})
                    }
                    deliveries = append(deliveries, adcp.MediaBuyDelivery{MediaBuyID: mbID, Status: buy.Status, Totals: adcp.DeliveryTotals{}, ByPackage: pkgDel})
                }
                return &adcp.DeliveryData{
                    ReportingPeriod: adcp.ReportingPeriod{Start: now.Add(-24 * time.Hour).Format(time.RFC3339), End: now.Format(time.RFC3339)},
                    MediaBuyDeliveries: deliveries,
                }, nil
            },
        })

        // Test controller for storyboard compliance testing
        adcp.RegisterTestController(server, &adcp.TestControllerStore{
            ForceAccountStatus: func(accountID, status string) (*adcp.StateTransition, error) {
                b.mu.Lock(); defer b.mu.Unlock()
                acct, ok := b.accounts[accountID]
                if !ok { return nil, fmt.Errorf("NOT_FOUND") }
                prev := acct.Status; acct.Status = status
                return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
            },
            ForceMediaBuyStatus: func(mediaBuyID, status, reason string) (*adcp.StateTransition, error) {
                b.mu.Lock(); defer b.mu.Unlock()
                buy, ok := b.mediaBuys[mediaBuyID]
                if !ok { return nil, fmt.Errorf("NOT_FOUND") }
                prev := buy.Status
                if prev == "completed" || prev == "rejected" || prev == "canceled" { return nil, fmt.Errorf("INVALID_TRANSITION") }
                buy.Status = status
                return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
            },
            ForceCreativeStatus: func(creativeID, status, reason string) (*adcp.StateTransition, error) {
                b.mu.Lock(); defer b.mu.Unlock()
                prev, ok := b.creatives[creativeID]
                if !ok { return nil, fmt.Errorf("NOT_FOUND") }
                b.creatives[creativeID] = status
                return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
            },
            SimulateDelivery: func(mediaBuyID string, p adcp.SimulateDeliveryParams) (*adcp.SimulationResult, error) {
                b.mu.Lock(); defer b.mu.Unlock()
                buy, ok := b.mediaBuys[mediaBuyID]
                if !ok { return nil, fmt.Errorf("NOT_FOUND") }
                var spend float64
                if p.ReportedSpend != nil { spend = p.ReportedSpend.Amount }
                for _, pkg := range buy.Packages {
                    ds := b.delivery[pkg.PackageID]
                    if ds == nil { ds = &struct{ Impressions, Clicks int; Spend float64 }{}; b.delivery[pkg.PackageID] = ds }
                    ds.Impressions += p.Impressions; ds.Clicks += p.Clicks; ds.Spend += spend
                }
                return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"impressions": p.Impressions, "clicks": p.Clicks, "spend": spend}}, nil
            },
            SimulateBudgetSpend: func(p adcp.SimulateBudgetParams) (*adcp.SimulationResult, error) {
                b.mu.Lock(); defer b.mu.Unlock()
                buy, ok := b.mediaBuys[p.MediaBuyID]
                if !ok { return nil, fmt.Errorf("NOT_FOUND") }
                var total float64
                for _, pkg := range buy.Packages { total += pkg.Budget }
                spend := total * p.SpendPercentage
                for _, pkg := range buy.Packages {
                    if total == 0 { continue }
                    ds := b.delivery[pkg.PackageID]
                    if ds == nil { ds = &struct{ Impressions, Clicks int; Spend float64 }{}; b.delivery[pkg.PackageID] = ds }
                    ds.Spend += spend * (pkg.Budget / total)
                }
                return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"spend": spend, "percentage": p.SpendPercentage}}, nil
            },
        })

        return server
    }))
}
```

## go.mod

```
module your-seller-agent

go 1.25

require (
    github.com/adcontextprotocol/adcp-go/adcp v0.0.0
    github.com/modelcontextprotocol/go-sdk v1.5.0
)
```

Then `go mod tidy`.

## Validation

```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_seller --json
```

Fix failures, repeat until all 9 steps pass.

## Returning Typed Errors

Handlers can return `adcp.NewError` for domain-specific errors instead of generic `INTERNAL_ERROR`:

```go
return nil, adcp.NewError("BUDGET_TOO_LOW", adcp.ErrorOptions{
    Message: "Budget $500 is below the $1,000 minimum for video",
    Field:   "budget",
})
```

Error codes with auto-recovery: `RATE_LIMITED` (retry), `BUDGET_TOO_LOW` / `INVALID_REQUEST` (revise), `ACCOUNT_NOT_FOUND` (terminal).

## Product Definitions

Each product needs: `ProductID`, `Name`, `Description`, `Channel`, `DeliveryType`, `PricingOptions`, `PublisherProperties`, `FormatIDs`.

Use lowercase pricing models: `"cpm"`, `"cpc"`, `"cpcv"`, not `"CPM"`.

**Broadcast/CTV products** include business terms:

```go
{
    ProductID: "primetime-30s", Name: "Primetime :30 — M-F 8-11pm",
    Description: "Primetime 30-second broadcast spots.", DeliveryType: "guaranteed",
    PricingOptions: []adcp.PricingOption{
        {PricingOptionID: "unit-30s", PricingModel: "unit", FixedPrice: 5000, Currency: "USD"},
    },
    PublisherProperties: []string{},
    FormatIDs: []adcp.FormatRef{{AgentURL: agentURL, ID: "broadcast-30s"}},
    CancellationPolicy: &adcp.CancellationPolicy{
        NoticePeriod:    adcp.Duration{Interval: 14, Unit: "days"},
        CancellationFee: adcp.CancellationFee{Type: "percent_remaining", Rate: 0.25},
    },
    MeasurementTerms: &adcp.MeasurementTerms{
        BillingMeasurement: &adcp.BillingMeasurement{
            Vendor: &adcp.BrandReference{Domain: "nielsen.com"}, MeasurementWindow: "c7",
        },
        MakegoodPolicy: &adcp.MakegoodPolicy{AvailableRemedies: []string{"additional_delivery", "credit"}},
    },
    PerformanceStandards: []adcp.PerformanceStandard{
        {Metric: "viewability", Threshold: 0.70, Standard: "mrc", Vendor: &adcp.BrandReference{Domain: "doubleverify.com"}},
    },
}
```

## Storyboards

| Storyboard | Use case |
|-----------|----------|
| `media_buy_seller` | Full lifecycle — pass this first |
| `media_buy_non_guaranteed` | Auction flow with bid adjustment |
| `media_buy_guaranteed_approval` | IO approval workflow |
| `media_buy_broadcast_seller` | Broadcast/CTV with measurement windows, Ad-ID, delayed delivery |
| `deterministic_testing` | Test controller state machines |

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Missing `IdempotencyReplayTTL` on `adcp.Config` | Required — set to `24*time.Hour`. Panics at startup if unset or outside 1h–7d. |
| Missing `Description` on products | Required by schema validation |
| Missing `publisher_properties`/`format_ids` on products | Required fields — use empty `[]string{}` if none |
| `sync_governance` response key `results` | Must be `accounts` |
| `sync_creatives` status `"accepted"` | Use `"approved"` — valid: processing, pending_review, approved, rejected, archived |
| Empty slices serialize as `null` | Use `make([]T, 0)` not `var x []T` |
| Uppercase pricing model | Use `"cpm"`, `"cpc"` not `"CPM"` |
| Ignoring buyer's `measurement_terms` on packages | Echo accepted terms back on confirmed package |

## SDK Reference

```go
import (
    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)
```

| Function | Usage |
|----------|-------|
| `adcp.Register(server, adcp.Config{...})` | Wire handlers — only set the tools you support. Auto-detects capabilities. |
| `adcp.Config.IdempotencyReplayTTL` | **Required.** How long you retain idempotency_key responses. Must be 1h–7d; 24h is standard. |
| `adcp.Config.Capabilities` | Optional typed CapabilitiesData — declare account / media_buy / audience_targeting blocks. Filled in automatically if nil. |
| `adcp.Config.ResolveAccount` | Automatic account resolution. Returns ACCOUNT_NOT_FOUND if nil. |
| `adcp.NewError(code, opts)` | Typed error from handlers (BUDGET_TOO_LOW, TERMS_REJECTED, etc.) |
| `adcp.Serve(createAgent)` | HTTP server on `:3001/mcp` |
| `adcp.RegisterTestController(server, store)` | Add comply_test_controller for storyboard testing |
| `adcp.AddTool(server, name, desc, handler)` | Register custom tools not covered by Config |

The skill contains everything you need. Do not read additional docs before writing code.
