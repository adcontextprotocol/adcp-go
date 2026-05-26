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
3. **Review pipeline?** Instant approve or pending review. When review is async, emit signed webhooks on approval/rejection transitions — see `skills/build-webhook-publisher/`. Artifact batch delivery also uses webhooks (`adcp.ArtifactWebhookPayload`).

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
adcp.AddTool(server, "list_creative_formats", "Available creative formats",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativeFormatsRequest) (*mcp.CallToolResult, any, error) {
        return adcp.CreativeFormatsResponse(formats, true)
    })
```

### 3. `sync_creatives`

Input has `creatives[]` with `creative_id`, `format_id`, `name`, `assets`. Store in memory with `created_date` and `updated_date` timestamps.

**Status must be a valid enum:** `"processing"`, `"pending_review"`, `"approved"`, `"rejected"`, `"archived"`. Use `"approved"` for instant-accept, NOT `"accepted"`.

```go
adcp.AddTool(server, "sync_creatives", "Accept and store creatives",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.SyncCreativesRequest) (*mcp.CallToolResult, any, error) {
        s.mu.Lock()
        defer s.mu.Unlock()

        now := time.Now().UTC().Format(time.RFC3339)
        var results []adcp.CreativeResult
        for _, c := range input.Creatives {
            action := "created"
            createdDate := now
            if existing, exists := s.creatives[c.CreativeID]; exists {
                action = "updated"
                createdDate = existing.CreatedDate
            }

            formatID := adcp.FormatRef{}
            if c.FormatID != nil {
                formatID = *c.FormatID
            }

            s.creatives[c.CreativeID] = &storedCreative{
                CreativeID:  c.CreativeID,
                Name:        c.Name,
                FormatID:    formatID,
                Status:      "approved",
                Assets:      c.Assets,
                CreatedDate: createdDate,
                UpdatedDate: now,
            }

            results = append(results, adcp.CreativeResult{
                CreativeID: c.CreativeID,
                Action:     action,
                Status:     "approved",
            })
        }

        return adcp.SyncCreativesResponse(results, true)
    })
```

### 4. `list_creatives`

**Required top-level fields:** `query_summary`, `pagination`, `creatives` — all handled by the builder.

Each creative must include `created_date` and `updated_date` (ISO 8601).

```go
adcp.AddTool(server, "list_creatives", "List creative library",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativesRequest) (*mcp.CallToolResult, any, error) {
        s.mu.RLock()
        defer s.mu.RUnlock()

        items := make([]map[string]any, 0)
        for _, c := range s.creatives {
            if input.Filters != nil && len(input.Filters.FormatIDs) > 0 {
                matched := false
                for _, fid := range input.Filters.FormatIDs {
                    if c.FormatID.AgentURL == fid.AgentURL && c.FormatID.ID == fid.ID {
                        matched = true
                        break
                    }
                }
                if !matched {
                    continue
                }
            }

            items = append(items, map[string]any{
                "creative_id":  c.CreativeID,
                "name":         c.Name,
                "format_id":    c.FormatID,
                "status":       "approved",
                "created_date": c.CreatedDate,
                "updated_date": c.UpdatedDate,
            })
        }

        return adcp.ListCreativesResponse(items)
    })
```

### 5. `preview_creative`

The request can come as `creative_id` (lookup) or `creative_manifest` (render directly). Handle both:

```go
adcp.AddTool(server, "preview_creative", "Render a preview",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.PreviewCreativeRequest) (*mcp.CallToolResult, any, error) {
        var creativeID, name string
        var w, h int

        if input.CreativeManifest != nil {
            // Render from manifest directly
            w, h = formatDimensions(input.CreativeManifest.FormatID.ID)
            creativeID = "preview-manifest"
        } else if input.CreativeID != "" {
            // Lookup from store
            s.mu.RLock()
            c, ok := s.creatives[input.CreativeID]
            s.mu.RUnlock()
            if !ok {
                return adcp.Errorf("NOT_FOUND", adcp.ErrorOptions{
                    Message: fmt.Sprintf("Creative %s not found", input.CreativeID),
                })
            }
            creativeID = c.CreativeID
            name = c.Name
            w, h = formatDimensions(c.FormatID.ID)
        }

        if name == "" { name = "Preview" }
        previewURL := "https://preview.example.com/" + creativeID
        return adcp.PreviewCreativeResponse(creativeID, name, previewURL, w, h)
    })
```

### 6. `build_creative`

Handle both `creative_manifest` (direct build) and `creative_id` (store lookup). If neither is provided but `target_format_id` is, build a default manifest from the format.

```go
adcp.AddTool(server, "build_creative", "Build serving tag",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.BuildCreativeRequest) (*mcp.CallToolResult, any, error) {
        var manifest map[string]any

        if input.CreativeManifest != nil {
            manifest = map[string]any{
                "format_id": input.CreativeManifest.FormatID,
                "assets": input.CreativeManifest.Assets,
            }
        } else if input.CreativeID != "" {
            s.mu.RLock()
            c, ok := s.creatives[input.CreativeID]
            s.mu.RUnlock()
            if !ok {
                return adcp.Errorf("NOT_FOUND", adcp.ErrorOptions{
                    Message: fmt.Sprintf("Creative %s not found", input.CreativeID),
                })
            }
            manifest = map[string]any{"format_id": c.FormatID, "name": c.Name, "assets": c.Assets}
        } else if input.TargetFormatID != nil {
            // Build default manifest from format
            manifest = map[string]any{"format_id": input.TargetFormatID, "assets": map[string]any{}}
        } else {
            return adcp.Errorf("INVALID_INPUT", adcp.ErrorOptions{
                Message: "creative_id, creative_manifest, or target_format_id required",
            })
        }

        return adcp.BuildCreativeResponse(manifest, true)
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
    FormatID    adcp.FormatRef
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

// formatDimensions returns the primary render dimensions for a format ID.
func formatDimensions(formatID string) (int, int) {
    for _, f := range formats {
        if f.FormatID.ID == formatID {
            if len(f.Renders) > 0 {
                return f.Renders[0].Width, f.Renders[0].Height
            }
        }
    }
    return 0, 0
}

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-creative", Version: "1.0.0"}, nil)
    // Register all 6 tools using adcp.AddTool and the response builders
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
| `list_creatives` missing `query_summary` | Use `adcp.ListCreativesResponse(items)` — it adds query_summary automatically |
| `list_creatives` missing `pagination` | Use `adcp.ListCreativesResponse(items)` — it adds pagination automatically |
| `list_creatives` missing dates | Each creative map needs `created_date` and `updated_date` (ISO 8601) |
| `preview_creative` missing `input.name` | Use `adcp.PreviewCreativeResponse(id, name, url, w, h)` — it adds input.name automatically |
| `list_creatives` ignores format filter | Check `input.Filters.FormatIDs` |
| No in-memory store | `list_creatives`/`preview_creative` need previously synced creatives |
| Manual `adcp.Result` for formats | Use `adcp.CreativeFormatsResponse(formats, true)` |
| Manual `adcp.Result` for build | Use `adcp.BuildCreativeResponse(manifest, true)` |

## SDK Reference

| Function | Usage |
|----------|-------|
| `adcp.AddTool(server, name, desc, handler)` | Register tool |
| `adcp.Serve(createAgent)` | HTTP server |
| `adcp.CapabilitiesResponse(data)` | Capabilities |
| `adcp.CreativeFormatsResponse(formats, sandbox)` | List creative formats response |
| `adcp.SyncCreativesResponse(results, sandbox)` | Sync creatives response |
| `adcp.ListCreativesResponse(items)` | List creatives with query_summary + pagination |
| `adcp.PreviewCreativeResponse(id, name, url, w, h)` | Preview creative response |
| `adcp.BuildCreativeResponse(manifest, sandbox)` | Build creative response |
| `adcp.Result(data, summary)` | Generic response |
| `adcp.Errorf(code, opts)` | Error response |

Input types: `adcp.EmptyInput`, `adcp.ListCreativeFormatsRequest`, `adcp.SyncCreativesRequest`, `adcp.ListCreativesRequest`, `adcp.PreviewCreativeRequest`, `adcp.BuildCreativeRequest`

The skill contains everything you need.
