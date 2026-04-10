---
name: build-generative-seller-agent
description: Use when building an AdCP generative seller in Go — an AI ad network or platform that sells inventory AND generates creatives from briefs.
---

# Build a Generative Seller Agent (Go)

## Overview

A generative seller does everything a standard seller does plus generates creatives from briefs. It MUST also accept standard IAB formats.

## When to Use

- User wants to build a generative DSP or AI ad network in Go

**Not this skill:** standard seller → `skills/build-seller-agent/`, standalone creative → `skills/build-creative-agent/`

## Before Writing Code

Same as seller skill, plus: what generative formats? Each needs a `brief` asset slot. What standard formats too?

## Implementation

**Read the seller skill first:** `skills/build-seller-agent/SKILL.md` — it has the complete pattern for all 9 tools. This skill only documents the deltas.

### Differences from seller

**`get_adcp_capabilities`** — same: `["media_buy", "compliance_testing"]`

**Products** — must include `description` (required field):
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
```

**`list_creative_formats`** — return BOTH generative and standard:
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
```

**`sync_creatives`** — check format to decide status:
```go
status := "approved"
if strings.Contains(formatID, "generative") {
    status = "pending_review"
}
```

Use `adcp.SyncCreativesResponse(results, true)` for the response.

### All other tools

Follow the seller skill exactly for: `sync_accounts`, `sync_governance`, `get_products`, `create_media_buy`, `get_media_buys`, `get_media_buy_delivery`, and compliance testing. All response shapes are identical.

## Validation

```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_generative_seller --json
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Only generative formats | Must also accept standard IAB formats |
| Same status for brief and standard | Generative → `"pending_review"`, standard → `"approved"` |
| Products missing `description` | Required field — storyboard validates it |
| All seller mistakes apply | See seller skill common mistakes table |

The skill contains everything you need. Read the seller skill for the complete base pattern.
