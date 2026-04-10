---
name: build-retail-media-agent
description: Use when building an AdCP retail media network agent in Go — sells on-site placements, supports product catalogs, tracks conversions.
---

# Build a Retail Media Agent (Go)

## Overview

A retail media agent sells advertising on a retailer's properties. It extends the standard seller with catalog sync, event tracking, and performance feedback.

## When to Use

- User wants to build a retail media network or commerce media platform in Go

**Not this skill:** standard seller → `skills/build-seller-agent/`, generative → `skills/build-generative-seller-agent/`

## Before Writing Code

Same as seller skill, plus: what catalogs, what conversion events, what feedback metrics?

## Implementation

**Read the seller skill first:** `skills/build-seller-agent/SKILL.md` — it has the complete pattern for all 9 seller tools.

### Critical: supported_protocols

The retail media storyboard checks for `conversion_tracking` capability. Without it, catalog and event tools are rejected.

```go
adcp.AddTool(server, "get_adcp_capabilities", "Agent capabilities",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
        return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
            ADCP: &adcp.ADCPVersion{MajorVersions: []int{3}},
            SupportedProtocols: []string{"media_buy", "conversion_tracking", "compliance_testing"},
        })
    })
```

### Products — must include `description`

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
```

### Additional tools (beyond seller's 9)

**10. `sync_catalogs`**

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
        return adcp.Result(map[string]any{"catalogs": results, "sandbox": true}, "Catalogs synced")
    })
```

Response JSON:
```json
{"catalogs": [{"catalog_id": "cat-1", "action": "created", "item_count": 10, "items_approved": 10}], "sandbox": true}
```

**11. `sync_event_sources`**

```go
adcp.AddTool(server, "sync_event_sources", "Register event tracking",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncEventSourcesInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.EventSourceResult
        for _, es := range input.EventSources {
            results = append(results, adcp.EventSourceResult{EventSourceID: es.EventSourceID, Action: "created"})
        }
        return adcp.Result(map[string]any{"event_sources": results, "sandbox": true}, "Event sources synced")
    })
```

**12. `log_event`**

```go
adcp.AddTool(server, "log_event", "Accept conversion events",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.LogEventInput) (*mcp.CallToolResult, any, error) {
        return adcp.Result(map[string]any{
            "events_received": len(input.Events), "events_processed": len(input.Events), "sandbox": true,
        }, fmt.Sprintf("Logged %d events", len(input.Events)))
    })
```

**13. `provide_performance_feedback`**

```go
adcp.AddTool(server, "provide_performance_feedback", "Accept performance metrics",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.PerformanceFeedbackInput) (*mcp.CallToolResult, any, error) {
        return adcp.Result(map[string]any{"success": true, "sandbox": true}, "Feedback received")
    })
```

### All other tools

Follow the seller skill exactly for: `sync_accounts`, `sync_governance`, `get_products`, `create_media_buy`, `get_media_buys`, `list_creative_formats`, `sync_creatives`, `get_media_buy_delivery`, and compliance testing.

## Validation

```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_catalog_creative --json
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Missing `conversion_tracking` in supported_protocols | Storyboard rejects catalog/event tools without it |
| Products missing `description` | Required field |
| `sync_catalogs` missing `item_count` | Required field |
| `log_event` missing `events_received` | Required counter |
| All seller mistakes apply | See seller skill common mistakes table |

The skill contains everything you need. Read the seller skill for the complete base pattern.
