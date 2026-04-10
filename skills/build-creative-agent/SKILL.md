---
name: build-creative-agent
description: Use when building an AdCP creative agent in Go — an ad server, creative management platform, or rendering service.
---

# Build a Creative Agent (Go)

## Overview

A creative agent manages the creative lifecycle: accepts assets, stores them in a library, builds serving tags, and renders previews.

## When to Use

- User wants to build an ad server, CMP, or creative rendering service in Go

**Not this skill:** selling inventory → `skills/build-seller-agent/`, audience data → `skills/build-signals-agent/`

## Before Writing Code

1. **What formats?** Display (300x250, 728x90), video (30s), native (image + text). Each needs dimensions and accepted asset types.
2. **What operations?** Sync (always), list, preview, build.
3. **Review pipeline?** Instant approve or pending review.

## Tool Registration

Use `adcp.AddTool` for all tools.

## Tools and Response Shapes

### 1. `get_adcp_capabilities`

```go
adcp.AddTool(server, "get_adcp_capabilities", "Agent capabilities",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
        return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
            ADCP: &adcp.ADCPVersion{MajorVersions: []int{3}},
            SupportedProtocols: []string{"creative"},
        })
    })
```

### 2. `list_creative_formats`

Asset `item_type` must be `"individual"`.

Response JSON:
```json
{"formats": [{"format_id": {"agent_url": "http://localhost:3001/mcp", "id": "display_300x250"}, "name": "Display 300x250", "renders": [{"width": 300, "height": 250}], "assets": [{"item_type": "individual", "asset_id": "image", "asset_type": "image", "required": true, "accepted_media_types": ["image/jpeg", "image/png"]}]}]}
```

### 3. `sync_creatives`

Input has `creatives[]` with `creative_id`, `format_id`, `name`, `assets`. Store in memory with `created_date` and `updated_date` timestamps.

**Status must be a valid enum:** `"processing"`, `"pending_review"`, `"approved"`, `"rejected"`, `"archived"`. Use `"approved"` for instant-accept, NOT `"accepted"`.

Response — include both `creatives` and `results` keys:
```go
return adcp.SyncCreativesResponse(results, true)
```

### 4. `list_creatives`

**Required top-level fields:** `query_summary`, `pagination`, `creatives`.

Each creative must include `created_date` and `updated_date` (ISO 8601).

```go
adcp.AddTool(server, "list_creatives", "List creative library",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativesInput) (*mcp.CallToolResult, any, error) {
        items := make([]map[string]any, 0)
        for _, c := range store.creatives {
            // apply input.Filters.FormatIDs if present
            items = append(items, map[string]any{
                "creative_id":  c.CreativeID,
                "name":         c.Name,
                "format_id":    c.FormatID,
                "status":       "approved",
                "created_date": c.CreatedDate,
                "updated_date": c.UpdatedDate,
            })
        }
        return adcp.Result(map[string]any{
            "query_summary": map[string]any{
                "total_matching": len(items),
                "returned":       len(items),
            },
            "pagination": map[string]any{
                "has_more":    false,
                "total_count": len(items),
            },
            "creatives": items,
        }, fmt.Sprintf("Found %d creatives", len(items)))
    })
```

### 5. `preview_creative`

Response must have `response_type: "single"`. Each preview must have `preview_id`, `renders` array, and `input` with required `name` field. Each render needs `render_id`, `output_format`, `preview_url` (for URL format), `role`.

```go
adcp.AddTool(server, "preview_creative", "Render a preview",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.PreviewCreativeInput) (*mcp.CallToolResult, any, error) {
        c := store.creatives[input.CreativeID]
        if c == nil {
            return adcp.Errorf("NOT_FOUND", adcp.ErrorOptions{Message: "Creative not found"})
        }
        return adcp.Result(map[string]any{
            "response_type": "single",
            "previews": []map[string]any{{
                "preview_id": "preview-" + c.CreativeID,
                "input":      map[string]any{"name": c.Name},
                "renders": []map[string]any{{
                    "render_id":     "render-" + c.CreativeID,
                    "output_format": "url",
                    "preview_url":   "https://preview.example.com/" + c.CreativeID,
                    "role":          "primary",
                    "dimensions":    map[string]any{"width": 300, "height": 250},
                }},
            }},
            "expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
        }, "Preview generated")
    })
```

### 6. `build_creative`

Response must include `creative_manifest`.

```go
adcp.AddTool(server, "build_creative", "Build serving tag",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.BuildCreativeInput) (*mcp.CallToolResult, any, error) {
        c := store.creatives[input.CreativeID]
        return adcp.Result(map[string]any{
            "creative_manifest": map[string]any{
                "format_id": c.FormatID, "name": c.Name, "assets": c.Assets,
            },
            "sandbox": true,
        }, "Creative built")
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

const agentURL = "http://localhost:3001/mcp"

type storedCreative struct {
    CreativeID  string
    Name        string
    FormatID    adcp.CreativeFormatID
    Status      string
    Assets      map[string]any
    CreatedDate string
    UpdatedDate string
}

type store struct {
    mu        sync.RWMutex
    creatives map[string]*storedCreative
}

var formats = []adcp.CreativeFormat{ /* define formats with item_type: "individual" */ }

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-creative", Version: "1.0.0"}, nil)
    // Register all 6 tools using adcp.AddTool
    return server
}

func main() {
    s := &store{creatives: make(map[string]*storedCreative)}
    log.Fatal(adcp.Serve(func() *mcp.Server { return createServer(s) }))
}
```

## Validation

```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp creative_lifecycle --json
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Status `"accepted"` | Use `"approved"` — valid enum: processing, pending_review, approved, rejected, archived |
| `list_creatives` missing `query_summary` | Required: `{total_matching: int, returned: int}` |
| `list_creatives` missing `pagination` | Required: `{has_more: bool, total_count: int}` |
| `list_creatives` missing dates | Each creative needs `created_date` and `updated_date` (ISO 8601) |
| `preview_creative` missing `input.name` | Required field in each preview's `input` object |
| `list_creatives` ignores format filter | Check `input.Filters.FormatIDs` |
| No in-memory store | `list_creatives`/`preview_creative` need previously synced creatives |

## SDK Reference

| Function | Usage |
|----------|-------|
| `adcp.AddTool(server, name, desc, handler)` | Register tool |
| `adcp.Serve(createAgent)` | HTTP server |
| `adcp.CapabilitiesResponse(data)` | Capabilities |
| `adcp.SyncCreativesResponse(results, sandbox)` | Sync creatives (dual-key) |
| `adcp.Result(data, summary)` | Generic response |
| `adcp.Errorf(code, opts)` | Error response |

Input types: `adcp.EmptyInput`, `adcp.ListCreativeFormatsInput`, `adcp.SyncCreativesInput`, `adcp.ListCreativesInput`, `adcp.PreviewCreativeInput`, `adcp.BuildCreativeInput`

The skill contains everything you need.
