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
3. **Products and pricing.** Each product needs: product_id, name, channel, delivery_type, pricing_options, publisher_properties (empty array OK), format_ids.
4. **Approval workflow.** Instant (`status: "active"`) or async (`status: "submitted"`, buyer polls `get_media_buys`).
5. **Creative management.** Standard (`list_creative_formats` + `sync_creatives`) or none.

## Tool Registration

Use `adcp.AddTool` for all tools. It generates typed JSON schemas from Go structs while accepting extra protocol fields that storyboards send.

```go
adcp.AddTool(server, "tool_name", "Description",
    func(ctx context.Context, req *mcp.CallToolRequest, input InputType) (*mcp.CallToolResult, any, error) {
        // input is already deserialized into InputType
        return adcp.Result(responseData, "summary")
    })
```

The SDK provides typed input structs (`adcp.SyncAccountsInput`, `adcp.GetProductsInput`, etc.) and response builders (`adcp.ProductsResponse`, `adcp.Result`, etc.).

## Tools and Response Shapes

Register in this order.

### 1. `get_adcp_capabilities`

```go
adcp.AddTool(server, "get_adcp_capabilities", "Returns agent capabilities",
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
adcp.AddTool(server, "sync_accounts", "Register advertiser accounts",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncAccountsInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.AccountResult
        for i, acct := range input.Accounts {
            id := fmt.Sprintf("acct-%s-%d", acct.Brand.Domain, i+1)
            results = append(results, adcp.AccountResult{
                AccountID: id, Brand: acct.Brand, Operator: acct.Operator,
                Action: "created", Status: "active",
            })
        }
        return adcp.Result(map[string]any{"accounts": results, "sandbox": true}, "Accounts synced")
    })
```

Response JSON:
```json
{"accounts": [{"account_id": "acct-acme.com-1", "brand": {"domain": "acme.com"}, "operator": "agency.com", "action": "created", "status": "active"}], "sandbox": true}
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
        return adcp.Result(map[string]any{"accounts": results}, "Governance synced")
    })
```

### 4. `get_products`

Products must include `publisher_properties` (empty array OK) and `format_ids`. Use lowercase pricing models (`"cpm"`, `"cpc"`).

```go
adcp.AddTool(server, "get_products", "Available advertising products",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetProductsInput) (*mcp.CallToolResult, any, error) {
        return adcp.ProductsResponse(&adcp.ProductsData{Products: products, Sandbox: true})
    })
```

Product definition:
```go
var products = []adcp.Product{
    {
        ProductID: "premium-display", Name: "Premium Display",
        Channel: "display", DeliveryType: "guaranteed",
        PricingOptions: []adcp.PricingOption{
            {PricingOptionID: "pd-cpm", PricingModel: "cpm", FixedPrice: 15.00, Currency: "USD"},
        },
        PublisherProperties: []string{},
        FormatIDs: []adcp.FormatRef{
            {AgentURL: "http://localhost:3001/mcp", ID: "banner-300x250"},
        },
    },
}
```

### 5. `create_media_buy`

```go
adcp.AddTool(server, "create_media_buy", "Create a media buy",
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
        return adcp.Result(map[string]any{"media_buys": buys, "sandbox": true}, "Media buys")
    })
```

### 7. `list_creative_formats`

Asset `item_type` must be `"individual"`.

```go
adcp.AddTool(server, "list_creative_formats", "Available creative formats",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativeFormatsInput) (*mcp.CallToolResult, any, error) {
        return adcp.Result(map[string]any{"formats": creativeFormats, "sandbox": true}, "Formats")
    })
```

### 8. `sync_creatives`

Include BOTH `creatives` and `results` keys in response.

```go
adcp.AddTool(server, "sync_creatives", "Submit creatives",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncCreativesInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.CreativeResult
        for _, c := range input.Creatives {
            results = append(results, adcp.CreativeResult{
                CreativeID: c.CreativeID, Action: "created", Status: "accepted",
            })
        }
        return adcp.Result(map[string]any{"creatives": results, "results": results, "sandbox": true}, "Synced")
    })
```

### 9. `get_media_buy_delivery`

Include both `media_buy_deliveries` and `media_buys` keys. Use `make([]T, 0)` for empty slices.

```go
adcp.AddTool(server, "get_media_buy_delivery", "Delivery metrics",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetMediaBuyDeliveryInput) (*mcp.CallToolResult, any, error) {
        deliveries := make([]adcp.MediaBuyDelivery, 0)
        // ... populate from store
        return adcp.Result(map[string]any{
            "reporting_period": adcp.ReportingPeriod{Start: start, End: end},
            "media_buy_deliveries": deliveries,
            "media_buys": deliveries,
        }, "Delivery data")
    })
```

## Compliance Testing

```go
adcp.RegisterTestController(server, &adcp.TestControllerStore{
    ForceAccountStatus: func(accountID, status string) (*adcp.StateTransition, error) {
        // Look up account, return NOT_FOUND if missing, swap status
        return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
    },
    ForceMediaBuyStatus: func(mediaBuyID, status, reason string) (*adcp.StateTransition, error) {
        // Same pattern. Check terminal states (completed/rejected/canceled) → INVALID_TRANSITION
    },
    ForceCreativeStatus: func(creativeID, status, reason string) (*adcp.StateTransition, error) {
        // Same pattern
    },
    SimulateDelivery: func(mediaBuyID string, p adcp.SimulateDeliveryParams) (*adcp.SimulationResult, error) {
        // Accumulate impressions/clicks/spend, return simulated + cumulative
    },
    SimulateBudgetSpend: func(p adcp.SimulateBudgetParams) (*adcp.SimulationResult, error) {
        // Calculate spend as percentage of total budget
    },
})
```

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

type store struct {
    mu        sync.RWMutex
    accounts  map[string]*adcp.AccountResult
    mediaBuys map[string]*adcp.MediaBuyData
    creatives map[string]string // id -> status
    delivery  map[string]*deliveryState
}
type deliveryState struct { Impressions, Clicks int; Spend float64 }

var products = []adcp.Product{ /* define products with PublisherProperties and FormatIDs */ }
var creativeFormats = []adcp.CreativeFormat{ /* define formats with item_type: "individual" */ }

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-seller", Version: "1.0.0"}, nil)

    // Register all 9 tools using adcp.AddTool
    // Register test controller
    adcp.RegisterTestController(server, &adcp.TestControllerStore{ /* ... */ })

    return server
}

func main() {
    s := &store{ /* init maps */ }
    log.Fatal(adcp.Serve(func() *mcp.Server { return createServer(s) }))
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

## Storyboards

| Storyboard | Use case |
|-----------|----------|
| `media_buy_seller` | Full lifecycle — pass this first |
| `media_buy_non_guaranteed` | Auction flow with bid adjustment |
| `media_buy_guaranteed_approval` | IO approval workflow |
| `deterministic_testing` | Test controller state machines |

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Using `mcp.AddTool` directly | Use `adcp.AddTool` — it generates permissive schemas |
| Missing `publisher_properties`/`format_ids` on products | Required fields |
| `sync_governance` response key `results` | Must be `accounts` |
| `sync_creatives` missing `results` key | Include both `creatives` and `results` |
| `get_delivery` returns `null` for empty arrays | Use `make([]T, 0)` not `var x []T` |
| `get_delivery` missing `media_buys` key | Include both `media_buy_deliveries` and `media_buys` |
| Uppercase pricing model | Use `"cpm"`, `"cpc"` not `"CPM"` |
| No mutex on maps | Use `sync.RWMutex` |

## SDK Reference

```go
import (
    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)
```

| Function | Usage |
|----------|-------|
| `adcp.AddTool(server, name, desc, handler)` | Register tool (typed input, permissive schema) |
| `adcp.Serve(createAgent)` | HTTP server on `:3001/mcp` |
| `adcp.RegisterTestController(server, store)` | Add comply_test_controller |
| `adcp.CapabilitiesResponse(data)` | Response builder |
| `adcp.ProductsResponse(data)` | Response builder |
| `adcp.MediaBuyResponse(data)` | Response builder |
| `adcp.DeliveryResponse(data)` | Response builder |
| `adcp.Result(data, summary)` | Generic response builder |
| `adcp.Error[T](code, opts)` | Error response |

Input types: `adcp.EmptyInput`, `adcp.SyncAccountsInput`, `adcp.SyncGovernanceInput`, `adcp.GetProductsInput`, `adcp.CreateMediaBuyInput`, `adcp.GetMediaBuysInput`, `adcp.ListCreativeFormatsInput`, `adcp.SyncCreativesInput`, `adcp.GetMediaBuyDeliveryInput`

The skill contains everything you need. Do not read additional docs before writing code.
