---
name: build-generative-seller-agent
description: Use when building an AdCP generative seller in Go — an AI ad network or platform that sells inventory AND generates creatives from briefs.
---

# Build a Generative Seller Agent (Go)

## Overview

A generative seller does everything a standard seller does (products, media buys, delivery) plus generates creatives from briefs. It MUST also accept standard IAB formats — the generative capability is additive.

## When to Use

- User wants to build a generative DSP or AI ad network in Go
- User mentions "creative from brief", "AI-generated ads", or "generative"

**Not this skill:** standard seller → `skills/build-seller-agent/`, standalone creative → `skills/build-creative-agent/`

## Before Writing Code

Same as seller, plus:
1. **Generative formats** — what formats does the platform generate? Each needs a `brief` asset slot.
2. **Standard formats** — must also accept pre-built assets (display images, VAST tags).
3. **Brand resolution** — validate brand domain on brief creatives. Reject if invalid.

## Tools

All 9 seller tools apply (see `skills/build-seller-agent/SKILL.md`). The differences:

### `get_adcp_capabilities`
```go
SupportedProtocols: []string{"media_buy", "compliance_testing"}
```

### `list_creative_formats` — BOTH generative and standard

```go
var creativeFormats = []adcp.CreativeFormat{
    {
        FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_300x250_generative"},
        Name: "Generated Display 300x250",
        Renders: []adcp.Render{{Width: 300, Height: 250}},
        Assets: []adcp.AssetSlot{
            {ItemType: "individual", AssetID: "brief", AssetType: "brief", Required: true, Description: "Creative brief"},
        },
    },
    {
        FormatID: adcp.CreativeFormatID{AgentURL: agentURL, ID: "display_300x250"},
        Name: "Display 300x250",
        Renders: []adcp.Render{{Width: 300, Height: 250}},
        Assets: []adcp.AssetSlot{
            {ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/jpeg", "image/png"}},
        },
    },
}
```

### `sync_creatives` — handle both brief and standard

Check format_id to decide processing path:
- Generative (format contains "generative"): status `"pending_review"` (async generation)
- Standard: status `"accepted"`

```go
status := "accepted"
if strings.Contains(fmtID, "generative") {
    status = "pending_review"
}
```

Include both `creatives` and `results` keys: `return adcp.SyncCreativesResponse(results, true)`

## All Other Tools

Follow the seller skill exactly for: `sync_accounts`, `sync_governance`, `get_products`, `create_media_buy`, `get_media_buys`, `get_media_buy_delivery`, and compliance testing.

Products must include `publisher_properties: []` and `format_ids`.

## Validation

```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_generative_seller --json
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Only generative formats | Must also accept standard IAB formats |
| Same status for brief and standard | Check format_id → "pending_review" for generative |
| All seller mistakes apply | See seller skill common mistakes table |

## SDK Reference

Same as seller skill. Input types: all seller input types. The skill contains everything you need.
