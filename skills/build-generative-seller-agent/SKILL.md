---
name: build-generative-seller-agent
description: Use when building an AdCP generative seller in Go — an AI ad network or platform that sells inventory AND generates creatives from briefs.
---

# Build a Generative Seller Agent (Go)

> **Status: Not yet validated against storyboard runner.** If validation fails, check the common mistakes table first, then file an issue.

## Overview

A generative seller does everything a standard seller does (products, media buys, delivery) plus generates creatives from briefs. It MUST also accept standard IAB formats — the generative capability is additive.

## Before Writing Code

Ask the user — don't guess.

1. **What kind of platform?** AI ad network, generative DSP, retail media with creative generation.
2. **Products and pricing.** Each product needs: product_id, name, description (required), channel, delivery_type, pricing_options, publisher_properties (empty array OK), format_ids. Use lowercase pricing models.
3. **Generative formats.** What does the platform generate? Each generative format needs a `brief` asset slot. Standard formats need traditional asset slots (image, video).
4. **Approval workflow.** Instant (`status: "active"`) or async (`status: "submitted"`). Async transitions SHOULD emit signed webhooks to `push_notification_config.url` — see `skills/build-webhook-publisher/`. Buyer polling is the legacy fallback only.

## Tool Registration

Use `adcp.AddTool` for all tools.

```go
adcp.AddTool(server, "tool_name", "Description",
    func(ctx context.Context, req *mcp.CallToolRequest, input InputType) (*mcp.CallToolResult, any, error) {
        return adcp.Result(data, "summary")
    })
```

## Tools and Response Shapes

### 1. `get_adcp_capabilities`

```go
adcp.AddTool(server, "get_adcp_capabilities", "Agent capabilities",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
        return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
            ADCP:               &adcp.ADCPVersion{MajorVersions: []int{3}},
            SupportedProtocols: []string{"media_buy", "compliance_testing"},
        })
    })
```

### 2. `sync_accounts`

Echo brand/operator back, assign an account_id.

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
            // store account...
        }
        return adcp.SyncAccountsResponse(results, true)
    })
```

### 3. `sync_governance`

Input has `accounts[]` with nested `account.brand`, `account.operator`, and `governance_agents[]`. Response key is `accounts`.

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

Products MUST include `description`, `publisher_properties`, and `format_ids`:

```go
var products = []adcp.Product{
    {
        ProductID: "ai-display", Name: "AI-Generated Display",
        Description: "AI-generated display ads from creative briefs",
        Channel: "display", DeliveryType: "non_guaranteed",
        PricingOptions: []adcp.PricingOption{
            {PricingOptionID: "ai-display-floor", PricingModel: "cpm", FloorPrice: 8.00, Currency: "USD"},
        },
        PublisherProperties: []string{},
        FormatIDs: []adcp.FormatRef{
            {AgentURL: agentURL, ID: "display_300x250_generative"},
            {AgentURL: agentURL, ID: "display_300x250"},
        },
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
        buys := make([]adcp.MediaBuyData, 0) // make, not var — ensures JSON [] not null
        // ... populate from store
        return adcp.MediaBuysResponse(buys, true)
    })
```

### 7. `list_creative_formats` — BOTH generative and standard

```go
var creativeFormats = []adcp.CreativeFormat{
    {   // Generative — brief asset
        FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_300x250_generative"},
        Name: "Generated Display 300x250",
        Renders: []adcp.Render{{Width: 300, Height: 250}},
        Assets: []adcp.AssetSlot{
            {ItemType: "individual", AssetID: "brief", AssetType: "brief", Required: true, Description: "Creative brief"},
        },
    },
    {   // Standard — image asset
        FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_300x250"},
        Name: "Display 300x250",
        Renders: []adcp.Render{{Width: 300, Height: 250}},
        Assets: []adcp.AssetSlot{
            {ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}},
        },
    },
}

adcp.AddTool(server, "list_creative_formats", "Available formats",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativeFormatsInput) (*mcp.CallToolResult, any, error) {
        return adcp.CreativeFormatsResponse(creativeFormats, true)
    })
```

### 8. `sync_creatives` — handle both brief and standard

Check format to decide status: generative → `"pending_review"`, standard → `"approved"`.

```go
adcp.AddTool(server, "sync_creatives", "Submit creatives",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncCreativesInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.CreativeResult
        for _, c := range input.Creatives {
            status := "approved"
            fmtID := ""
            if c.FormatID != nil { fmtID = c.FormatID.ID }
            if strings.Contains(fmtID, "generative") {
                status = "pending_review"
            }
            results = append(results, adcp.CreativeResult{
                CreativeID: c.CreativeID, Action: "created", Status: status,
            })
        }
        return adcp.SyncCreativesResponse(results, true)
    })
```

### 9. `get_media_buy_delivery`

Use `make([]T, 0)` for empty slices to ensure JSON `[]` not `null`.

```go
adcp.AddTool(server, "get_media_buy_delivery", "Delivery metrics",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetMediaBuyDeliveryInput) (*mcp.CallToolResult, any, error) {
        deliveries := make([]adcp.MediaBuyDelivery, 0)
        // ... populate from store
        return adcp.DeliveryResponse(&adcp.DeliveryData{
            ReportingPeriod: adcp.ReportingPeriod{Start: start, End: end},
            MediaBuyDeliveries: deliveries,
        })
    })
```

## Compliance Testing

```go
adcp.RegisterTestController(server, &adcp.TestControllerStore{
    ForceAccountStatus: func(accountID, status string) (*adcp.StateTransition, error) {
        // Look up, return NOT_FOUND if missing, swap status
    },
    ForceMediaBuyStatus: func(mediaBuyID, status, reason string) (*adcp.StateTransition, error) {
        // Same pattern. Check terminal states → INVALID_TRANSITION
    },
    ForceCreativeStatus: func(creativeID, status, reason string) (*adcp.StateTransition, error) { /* same */ },
    SimulateDelivery: func(mediaBuyID string, p adcp.SimulateDeliveryParams) (*adcp.SimulationResult, error) { /* accumulate */ },
    SimulateBudgetSpend: func(p adcp.SimulateBudgetParams) (*adcp.SimulationResult, error) { /* calculate */ },
})
```

## Complete Skeleton

```go
package main

import (
    "context"
    "fmt"
    "log"
    "strings"
    "sync"
    "time"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

const agentURL = "http://localhost:3001/mcp"

type store struct {
    mu        sync.RWMutex
    accounts  map[string]*adcp.AccountResult
    mediaBuys map[string]*adcp.MediaBuyData
    creatives map[string]string // id -> status
    delivery  map[string]*deliveryState
}
type deliveryState struct { Impressions, Clicks int; Spend float64 }

var products = []adcp.Product{ /* define with Description, PublisherProperties, FormatIDs */ }
var creativeFormats = []adcp.CreativeFormat{ /* both generative + standard */ }

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-gen-seller", Version: "1.0.0"}, nil)
    // Register all 9 tools + test controller
    return server
}

func main() {
    s := &store{accounts: make(map[string]*adcp.AccountResult), mediaBuys: make(map[string]*adcp.MediaBuyData), creatives: make(map[string]string), delivery: make(map[string]*deliveryState)}
    log.Fatal(adcp.Serve(func() *mcp.Server { return createServer(s) }))
}
```

## go.mod

```
module your-generative-seller
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
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_generative_seller --json
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Using `mcp.AddTool` directly | Use `adcp.AddTool` |
| Only generative formats | Must also accept standard IAB formats |
| Same status for brief and standard | Generative → `"pending_review"`, standard → `"approved"` |
| Products missing `description` | Required field |
| Missing `publisher_properties`/`format_ids` | Required fields |
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
| `adcp.Result(data, summary)` | Generic response |
| `adcp.Errorf(code, opts)` | Error response |

Input types: `adcp.EmptyInput`, `adcp.SyncAccountsInput`, `adcp.SyncGovernanceInput`, `adcp.GetProductsInput`, `adcp.CreateMediaBuyInput`, `adcp.GetMediaBuysInput`, `adcp.ListCreativeFormatsInput`, `adcp.SyncCreativesInput`, `adcp.GetMediaBuyDeliveryInput`

The skill contains everything you need.
