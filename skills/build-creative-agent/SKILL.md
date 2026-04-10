---
name: build-creative-agent
description: Use when building an AdCP creative agent in Go — an ad server, creative management platform, or rendering service.
---

# Build a Creative Agent (Go)

## Overview

A creative agent manages the creative lifecycle: accepts assets, stores them, builds serving tags, and renders previews. It does not sell media.

## When to Use

- User wants to build an ad server, CMP, or creative rendering service in Go
- User mentions sync_creatives, preview_creative, build_creative, or creative formats

**Not this skill:** selling inventory → `skills/build-seller-agent/`, audience data → `skills/build-signals-agent/`

## Before Writing Code

1. **What formats?** Display (300x250, 728x90), video (30s), native (image + text). Each needs dimensions and accepted asset types.
2. **What operations?** Sync (always), list, preview, build.
3. **Review pipeline?** Instant accept, pending review, or rejection.

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

```go
adcp.AddTool(server, "list_creative_formats", "Available formats",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativeFormatsInput) (*mcp.CallToolResult, any, error) {
        return adcp.Result(map[string]any{"formats": formats}, "Formats")
    })
```

Response JSON:
```json
{
  "formats": [{
    "format_id": {"agent_url": "http://localhost:3001/mcp", "id": "display_300x250"},
    "name": "Display 300x250",
    "renders": [{"width": 300, "height": 250}],
    "assets": [{"item_type": "individual", "asset_id": "image", "asset_type": "image", "required": true, "accepted_media_types": ["image/jpeg", "image/png"]}]
  }]
}
```

### 3. `sync_creatives`

Input has `creatives[]` with `creative_id`, `format_id`, `name`, `assets`. Store in memory. Return both `creatives` and `results` keys.

```go
adcp.AddTool(server, "sync_creatives", "Accept and store creatives",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncCreativesInput) (*mcp.CallToolResult, any, error) {
        var results []adcp.CreativeResult
        for _, c := range input.Creatives {
            // store creative, determine action "created"/"updated"
            results = append(results, adcp.CreativeResult{
                CreativeID: c.CreativeID, Action: action, Status: "accepted",
            })
        }
        return adcp.SyncCreativesResponse(results, true)
    })
```

### 4. `list_creatives`

Support `filters.format_ids` filtering. Return stored creatives.

```go
adcp.AddTool(server, "list_creatives", "List creative library",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativesInput) (*mcp.CallToolResult, any, error) {
        items := make([]adcp.CreativeListItem, 0)
        // filter by input.Filters.FormatIDs if present
        return adcp.Result(map[string]any{"creatives": items}, "Creatives")
    })
```

### 5. `preview_creative`

Look up stored creative. Return `response_type: "single"`, preview with render.

```go
adcp.AddTool(server, "preview_creative", "Render a preview",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.PreviewCreativeInput) (*mcp.CallToolResult, any, error) {
        // look up input.CreativeID from store
        result := &adcp.PreviewResult{
            ResponseType: "single",
            Previews: []adcp.Preview{{
                PreviewID: "preview-" + input.CreativeID,
                Input: map[string]any{"format_id": c.FormatID, "name": c.Name},
                Renders: []adcp.PreviewRender{{
                    RenderID: "render-" + input.CreativeID,
                    OutputFormat: "url",
                    PreviewURL: "https://preview.example.com/" + input.CreativeID,
                    Role: "primary",
                    Dimensions: &adcp.Render{Width: 300, Height: 250},
                }},
            }},
            ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
        }
        return adcp.Result(result, "Preview generated")
    })
```

### 6. `build_creative`

Look up stored creative. Return `creative_manifest`.

```go
adcp.AddTool(server, "build_creative", "Build serving tag",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.BuildCreativeInput) (*mcp.CallToolResult, any, error) {
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
    CreativeID string
    Name       string
    FormatID   adcp.CreativeFormatID
    Status     string
    Assets     map[string]any
}

type store struct {
    mu        sync.RWMutex
    creatives map[string]*storedCreative
}

var formats = []adcp.CreativeFormat{ /* define formats */ }

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-creative", Version: "1.0.0"}, nil)
    // Register all 6 tools
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
| Using `mcp.AddTool` directly | Use `adcp.AddTool` |
| `list_creatives` ignores format filter | Check `input.Filters.FormatIDs` |
| `preview_creative` wrong response_type | Must be `"single"` |
| `build_creative` missing creative_manifest | Required field |
| No in-memory store | `list_creatives`/`preview_creative` need previously synced creatives |
| `sync_creatives` missing `results` key | Use `adcp.SyncCreativesResponse` which includes both |

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
