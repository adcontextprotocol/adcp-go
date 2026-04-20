---
name: build-retail-media-agent
description: Use when building an AdCP retail media network agent in Go — sells on-site placements, supports product catalogs, tracks conversions.
---

# Build a Retail Media Agent (Go)

> **Status: Not yet validated against storyboard runner.** If validation fails, check the common mistakes table first, then file an issue.

## Overview

A retail media agent sells advertising on a retailer's properties. It extends the standard seller with catalog sync, event tracking, and performance feedback.

## Before Writing Code

1. **Products.** Each needs: product_id, name, description (required), channel, delivery_type, pricing_options, publisher_properties, format_ids. Use lowercase pricing models.
2. **Catalogs.** What product feeds? How connected to ad rendering?
3. **Events.** What conversion events? Purchase, add_to_cart, page_view?
4. **Approval workflow.** Instant or async. Async SHOULD emit signed webhooks when the buyer supplies `push_notification_config` — see `skills/build-webhook-publisher/` for the emission pattern.

## Tool Registration

Use `adcp.AddTool` for all 13 tools.

## Tools and Response Shapes

### 1. `get_adcp_capabilities` — MUST include `conversion_tracking`

```go
adcp.AddTool(server, "get_adcp_capabilities", "Agent capabilities",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
        return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
            ADCP:               &adcp.ADCPVersion{MajorVersions: []int{3}},
            SupportedProtocols: []string{"media_buy", "conversion_tracking", "compliance_testing"},
        })
    })
```

### 2. `sync_accounts`

```go
adcp.AddTool(server, "sync_accounts", "Register accounts",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncAccountsInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.AccountResult
        for i, acct := range input.Accounts {
            id := fmt.Sprintf("acct-%s-%d", acct.Brand.Domain, i+1)
            results = append(results, adcp.AccountResult{
                AccountID: id, Brand: acct.Brand, Operator: acct.Operator,
                Action: "created", Status: "active",
            })
        }
        return adcp.SyncAccountsResponse(results, true)
    })
```

### 3. `sync_governance`

```go
adcp.AddTool(server, "sync_governance", "Register governance agents",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncGovernanceInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.GovernanceResult
        for _, acct := range input.Accounts {
            govAcct := acct.Account
            if govAcct == nil {
                govAcct = &adcp.GovernanceAccount{Brand: acct.Brand, Operator: acct.Operator}
            }
            results = append(results, adcp.GovernanceResult{
                Account: govAcct, Status: "synced", GovernanceAgents: acct.GovernanceAgents,
            })
        }
        return adcp.GovernanceResponse(results)
    })
```

### 4. `get_products`

```go
var products = []adcp.Product{
    {
        ProductID: "sponsored-product", Name: "Sponsored Product",
        Description: "Promoted product listings in search results",
        Channel: "retail_media", DeliveryType: "non_guaranteed",
        PricingOptions: []adcp.PricingOption{
            {PricingOptionID: "sp-cpc", PricingModel: "cpc", FixedPrice: 0.50, Currency: "USD"},
        },
        PublisherProperties: []string{},
        FormatIDs: []adcp.FormatRef{{AgentURL: agentURL, ID: "product-card"}},
    },
}

adcp.AddTool(server, "get_products", "Available products",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetProductsInput) (*mcp.CallToolResult, any, error) {
        return adcp.ProductsResponse(&adcp.ProductsData{Products: products, Sandbox: true})
    })
```

### 5. `create_media_buy`

```go
adcp.AddTool(server, "create_media_buy", "Create media buy",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.CreateMediaBuyInput) (*mcp.CallToolResult, any, error) {
        id := fmt.Sprintf("mb-%d", counter)
        var pkgs []adcp.Package
        for i, p := range input.Packages {
            pkgs = append(pkgs, adcp.Package{
                PackageID: fmt.Sprintf("%s-pkg-%d", id, i+1),
                ProductID: p.ProductID, PricingOptionID: p.PricingOptionID, Budget: p.Budget,
            })
        }
        return adcp.MediaBuyResponse(&adcp.MediaBuyData{
            MediaBuyID: id, Status: "active", Currency: "USD", Packages: pkgs,
        })
    })
```

### 6. `get_media_buys`

```go
adcp.AddTool(server, "get_media_buys", "List media buys",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetMediaBuysInput) (*mcp.CallToolResult, any, error) {
        buys := make([]adcp.MediaBuyData, 0)
        // populate from store
        return adcp.MediaBuysResponse(buys, true)
    })
```

### 7. `list_creative_formats`

```go
adcp.AddTool(server, "list_creative_formats", "Available formats",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativeFormatsInput) (*mcp.CallToolResult, any, error) {
        return adcp.CreativeFormatsResponse(creativeFormats, true)
    })
```

### 8. `sync_creatives`

```go
adcp.AddTool(server, "sync_creatives", "Submit creatives",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncCreativesInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.CreativeResult
        for _, c := range input.Creatives {
            results = append(results, adcp.CreativeResult{
                CreativeID: c.CreativeID, Action: "created", Status: "approved",
            })
        }
        return adcp.SyncCreativesResponse(results, true)
    })
```

### 9. `get_media_buy_delivery`

```go
adcp.AddTool(server, "get_media_buy_delivery", "Delivery metrics",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetMediaBuyDeliveryInput) (*mcp.CallToolResult, any, error) {
        deliveries := make([]adcp.MediaBuyDelivery, 0)
        // populate from store
        return adcp.DeliveryResponse(&adcp.DeliveryData{
            ReportingPeriod: adcp.ReportingPeriod{Start: start, End: end},
            MediaBuyDeliveries: deliveries,
        })
    })
```

### 10. `sync_catalogs`

```go
adcp.AddTool(server, "sync_catalogs", "Accept product catalog feeds",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncCatalogsInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.CatalogResult
        for _, c := range input.Catalogs {
            count := len(c.Items)
            if count == 0 { count = 10 }
            results = append(results, adcp.CatalogResult{
                CatalogID: c.CatalogID, Action: "created", ItemCount: count, ItemsApproved: count,
            })
        }
        return adcp.SyncCatalogsResponse(results, true)
    })
```

### 11. `sync_event_sources`

```go
adcp.AddTool(server, "sync_event_sources", "Register event tracking",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncEventSourcesInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.EventSourceResult
        for _, es := range input.EventSources {
            results = append(results, adcp.EventSourceResult{
                EventSourceID: es.EventSourceID, Action: "created",
                Setup: &adcp.EventSourceSetup{
                    Snippet:     fmt.Sprintf("<script src=\"https://track.example.com/%s.js\"></script>", es.EventSourceID),
                    Description: "Add this snippet to your checkout page",
                },
            })
        }
        return adcp.SyncEventSourcesResponse(results, true)
    })
```

### 12. `log_event`

```go
adcp.AddTool(server, "log_event", "Accept conversion events",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.LogEventInput) (*mcp.CallToolResult, any, error) {
        return adcp.LogEventResponse(len(input.Events), len(input.Events), 0.85, true)
    })
```

### 13. `provide_performance_feedback`

```go
adcp.AddTool(server, "provide_performance_feedback", "Accept performance metrics",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.PerformanceFeedbackInput) (*mcp.CallToolResult, any, error) {
        return adcp.PerformanceFeedbackResponse(true)
    })
```

## Compliance Testing

Same pattern as seller — `adcp.RegisterTestController(server, store)` with ForceAccountStatus, ForceMediaBuyStatus, ForceCreativeStatus, SimulateDelivery, SimulateBudgetSpend.

## Complete Skeleton

```go
package main

import (
    "context"
    "fmt"
    "log"
    "sync"
    "time"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

const agentURL = "http://localhost:3001/mcp"

type store struct {
    mu           sync.RWMutex
    accounts     map[string]*adcp.AccountResult
    mediaBuys    map[string]*adcp.MediaBuyData
    creatives    map[string]string
    delivery     map[string]*deliveryState
    catalogs     map[string]int
    eventSources map[string]bool
}
type deliveryState struct { Impressions, Clicks int; Spend float64 }

var products = []adcp.Product{ /* with Description, PublisherProperties, FormatIDs */ }
var creativeFormats = []adcp.CreativeFormat{ /* with item_type: "individual" */ }

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-retail", Version: "1.0.0"}, nil)
    // Register all 13 tools + test controller
    return server
}

func main() {
    s := &store{accounts: make(map[string]*adcp.AccountResult), mediaBuys: make(map[string]*adcp.MediaBuyData), creatives: make(map[string]string), delivery: make(map[string]*deliveryState), catalogs: make(map[string]int), eventSources: make(map[string]bool)}
    log.Fatal(adcp.Serve(func() *mcp.Server { return createServer(s) }))
}
```

## go.mod

```
module your-retail-media
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
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_catalog_creative --json
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Using `mcp.AddTool` directly | Use `adcp.AddTool` |
| Missing `conversion_tracking` in supported_protocols | Storyboard rejects catalog/event tools without it |
| Products missing `description` | Required field |
| Missing `publisher_properties`/`format_ids` | Required fields |
| `sync_catalogs` missing `item_count` | Required field |
| `log_event` missing `events_received` | Required counter |
| `log_event` missing `match_quality` | Include match quality score (0.0-1.0) via `adcp.LogEventResponse` |
| `sync_event_sources` missing `setup.snippet` | Include `Setup` with integration snippet on each event source |
| `sync_governance` response key `results` | Must be `accounts` |
| `get_delivery` returns `null` for empty arrays | Use `make([]T, 0)` |
| `get_delivery` returns `null` for empty deliveries | Use `adcp.DeliveryResponse` |
| Uppercase pricing model | Use `"cpm"`, `"cpc"` not `"CPM"` |
| No mutex on maps | Use `sync.RWMutex` |

## SDK Reference

| Function | Usage |
|----------|-------|
| `adcp.AddTool(server, name, desc, handler)` | Register tool |
| `adcp.Serve(createAgent)` | HTTP server |
| `adcp.RegisterTestController(server, store)` | Test controller |
| `adcp.CapabilitiesResponse(data)` | Capabilities |
| `adcp.ProductsResponse(data)` | Products |
| `adcp.MediaBuyResponse(data)` | Create media buy |
| `adcp.MediaBuysResponse(buys, sandbox)` | List media buys |
| `adcp.DeliveryResponse(data)` | Delivery metrics |
| `adcp.SyncAccountsResponse(accounts, sandbox)` | Sync accounts |
| `adcp.GovernanceResponse(accounts)` | Sync governance |
| `adcp.CreativeFormatsResponse(formats, sandbox)` | Creative formats |
| `adcp.SyncCreativesResponse(creatives, sandbox)` | Sync creatives |
| `adcp.SyncCatalogsResponse(catalogs, sandbox)` | Sync catalogs |
| `adcp.SyncEventSourcesResponse(sources, sandbox)` | Event sources |
| `adcp.LogEventResponse(received, processed, sandbox)` | Log events |
| `adcp.PerformanceFeedbackResponse(sandbox)` | Performance feedback |
| `adcp.Result(data, summary)` | Generic response |
| `adcp.Errorf(code, opts)` | Error response |

Input types: `adcp.EmptyInput`, `adcp.SyncAccountsInput`, `adcp.SyncGovernanceInput`, `adcp.GetProductsInput`, `adcp.CreateMediaBuyInput`, `adcp.GetMediaBuysInput`, `adcp.ListCreativeFormatsInput`, `adcp.SyncCreativesInput`, `adcp.GetMediaBuyDeliveryInput`, `adcp.SyncCatalogsInput`, `adcp.SyncEventSourcesInput`, `adcp.LogEventInput`, `adcp.PerformanceFeedbackInput`

The skill contains everything you need.
