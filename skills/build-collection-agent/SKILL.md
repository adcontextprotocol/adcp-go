---
name: build-collection-agent
description: Use when building an AdCP collection agent in Go — a governance provider, data company, or agency that manages curated lists of content collections (TV shows, podcasts, publications) for targeting and brand safety.
---

# Build a Collection Agent (Go)

## Overview

A collection agent manages curated lists of content collections (programs, shows, publications) that buyers and sellers reference for targeting, exclusions, and brand safety. Lists can be shared across agents via auth tokens and include dynamic filters that are resolved at setup time.

## When to Use

- User wants to build an agent that manages content collection lists in Go
- User mentions collection lists, show lists, program targeting, CTV content curation, or brand safety lists
- User needs cross-publisher content matching (distribution identifiers like IMDB, Gracenote, EIDR)

**Not this skill:** selling inventory -> `skills/build-seller-agent/`, audience signals -> `skills/build-signals-agent/`, creative rendering -> `skills/build-creative-agent/`

## Before Writing Code

Ask the user -- don't guess.

1. **What content?** TV shows, podcasts, publications, events? This determines the `kind` values (`series`, `publication`, `event_series`, `rotation`).
2. **Selection patterns?** Cross-publisher by distribution IDs (IMDB, Gracenote) / publisher-specific collection IDs / publisher-specific genres? Most agents support at least distribution IDs.
3. **What filters?** Content ratings, genres, production quality? Which genre taxonomy (`iab_content_3.0`, `gracenote`, `custom`)?
4. **Webhook notifications?** Should consumers be notified when resolved lists change?
5. **Auth model?** Auth tokens are returned at creation time. Should lists be public or require tokens?

## Tool Registration

Use `adcp.AddTool` for all tools. It generates typed JSON schemas from Go structs while accepting extra protocol fields that storyboards send.

```go
adcp.AddTool(server, "tool_name", "Description",
    func(ctx context.Context, req *mcp.CallToolRequest, input InputType) (*mcp.CallToolResult, any, error) {
        return adcp.Result(responseData, "summary")
    })
```

The SDK provides typed input structs (`adcp.CreateCollectionListRequest`, `adcp.GetCollectionListRequest`, etc.) and response builders (`adcp.CreateCollectionListResponse`, `adcp.GetCollectionListResponse`, etc.).

## Tools and Response Shapes

Register in this order.

### 1. `get_adcp_capabilities`

```go
adcp.AddTool(server, "get_adcp_capabilities", "Returns agent capabilities",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
        return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
            ADCP:               &adcp.ADCPVersion{MajorVersions: []int{3}},
            SupportedProtocols: []string{"collection"},
        })
    })
```

### 2. `create_collection_list`

Base collections use a discriminated union with three selection patterns. Use the constructor functions:

```go
adcp.AddTool(server, "create_collection_list", "Create a managed collection list",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.CreateCollectionListRequest) (*mcp.CallToolResult, any, error) {
        listID := fmt.Sprintf("list-%s", uuid())
        authToken := generateToken(listID)

        list := &adcp.CollectionList{
            ListID:          listID,
            Name:            input.Name,
            Description:     input.Description,
            BaseCollections: input.BaseCollections,
            Filters:         input.Filters,
            CreatedAt:       time.Now().UTC().Format(time.RFC3339),
            UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
        }
        if input.Brand != nil {
            list.Brand = input.Brand
        }

        // Resolve collections and set count
        resolved := resolveCollections(list)
        list.CollectionCount = len(resolved)

        // Store list and token
        s.mu.Lock()
        s.lists[listID] = list
        s.tokens[listID] = authToken
        s.resolved[listID] = resolved
        s.mu.Unlock()

        return adcp.CreateCollectionListResponse(list, authToken)
    })
```

**Base collection constructor functions:**

```go
// Cross-publisher matching by distribution IDs
adcp.ByDistributionIDs([]adcp.DistributionID{
    {Type: "imdb_id", Value: "tt0903747"},      // Breaking Bad
    {Type: "gracenote_id", Value: "SH01234"},
})

// Specific collections from a publisher
adcp.ByPublisherCollections("hulu.com", []string{"comedy-originals", "drama-catalog"})

// All content matching genres from a publisher
adcp.ByPublisherGenres("roku.com", []string{"Comedy", "Drama"}, "iab_content_3.0")
```

Response JSON:
```json
{
  "list": {
    "list_id": "list-abc123",
    "name": "Premium Shows",
    "base_collections": [
      {"selection_type": "distribution_ids", "identifiers": [{"type": "imdb_id", "value": "tt0903747"}]}
    ],
    "collection_count": 42,
    "created_at": "2026-04-16T00:00:00Z",
    "updated_at": "2026-04-16T00:00:00Z"
  },
  "auth_token": "eyJ..."
}
```

**Important:** `auth_token` is required in the create response. It is only returned at creation time.

### 3. `get_collection_list`

Returns list metadata and optionally resolved collections. The `resolve` field defaults to `true` when absent.

```go
adcp.AddTool(server, "get_collection_list", "Retrieve a collection list with resolved collections",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetCollectionListRequest) (*mcp.CallToolResult, any, error) {
        s.mu.RLock()
        list, ok := s.lists[input.ListID]
        resolved := s.resolved[input.ListID]
        s.mu.RUnlock()

        if !ok {
            return adcp.Error[adcp.GetCollectionListRequest]("LIST_NOT_FOUND",
                adcp.WithMessage("Collection list not found"),
                adcp.WithRecovery("terminal"))
        }

        // resolve defaults to true
        shouldResolve := input.Resolve == nil || *input.Resolve
        if !shouldResolve {
            return adcp.GetCollectionListResponse(list, nil, nil)
        }

        // Apply pagination
        page := resolved
        var pagination *adcp.PaginationResponse
        if input.Pagination != nil && input.Pagination.MaxResults > 0 && len(resolved) > input.Pagination.MaxResults {
            page = resolved[:input.Pagination.MaxResults]
            pagination = &adcp.PaginationResponse{HasMore: true, Cursor: "next", TotalCount: len(resolved)}
        }

        return adcp.GetCollectionListResponse(list, page, pagination)
    })
```

Response JSON (with collections):
```json
{
  "list": {"list_id": "list-abc123", "name": "Premium Shows", "collection_count": 42},
  "collections": [
    {
      "name": "Breaking Bad",
      "collection_rid": "col-bb",
      "distribution_ids": [{"type": "imdb_id", "value": "tt0903747"}],
      "content_rating": {"system": "us_tv", "rating": "TV-14"},
      "genre": ["Drama", "Crime"],
      "genre_taxonomy": "iab_content_3.0",
      "kind": "series"
    }
  ],
  "pagination": {"has_more": true, "cursor": "next", "total_count": 42}
}
```

### 4. `update_collection_list`

All fields except `list_id` are optional. `base_collections` and `filters` are **full replacements, not patches**.

```go
adcp.AddTool(server, "update_collection_list", "Update an existing collection list",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.UpdateCollectionListRequest) (*mcp.CallToolResult, any, error) {
        s.mu.Lock()
        defer s.mu.Unlock()

        list, ok := s.lists[input.ListID]
        if !ok {
            return adcp.Error[adcp.UpdateCollectionListRequest]("LIST_NOT_FOUND",
                adcp.WithMessage("Collection list not found"),
                adcp.WithRecovery("terminal"))
        }

        if input.Name != "" { list.Name = input.Name }
        if input.Description != "" { list.Description = input.Description }
        if input.BaseCollections != nil { list.BaseCollections = input.BaseCollections }
        if input.Filters != nil { list.Filters = input.Filters }
        if input.Brand != nil { list.Brand = input.Brand }
        if input.WebhookURL != "" { list.WebhookURL = input.WebhookURL }
        list.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

        // Re-resolve collections
        resolved := resolveCollections(list)
        list.CollectionCount = len(resolved)
        s.resolved[input.ListID] = resolved

        return adcp.UpdateCollectionListResponse(list)
    })
```

### 5. `delete_collection_list`

```go
adcp.AddTool(server, "delete_collection_list", "Delete a collection list",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.DeleteCollectionListRequest) (*mcp.CallToolResult, any, error) {
        s.mu.Lock()
        _, ok := s.lists[input.ListID]
        if ok {
            delete(s.lists, input.ListID)
            delete(s.tokens, input.ListID)
            delete(s.resolved, input.ListID)
        }
        s.mu.Unlock()

        if !ok {
            return adcp.Error[adcp.DeleteCollectionListRequest]("LIST_NOT_FOUND",
                adcp.WithMessage("Collection list not found"),
                adcp.WithRecovery("terminal"))
        }
        return adcp.DeleteCollectionListResponse(input.ListID)
    })
```

### 6. `list_collection_lists`

```go
adcp.AddTool(server, "list_collection_lists", "List available collection lists",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCollectionListsRequest) (*mcp.CallToolResult, any, error) {
        s.mu.RLock()
        defer s.mu.RUnlock()

        results := make([]adcp.CollectionList, 0)
        for _, list := range s.lists {
            if input.Principal != "" && list.Principal != input.Principal { continue }
            if input.NameContains != "" && !strings.Contains(list.Name, input.NameContains) { continue }
            results = append(results, *list)
        }
        return adcp.ListCollectionListsResponse(results, nil)
    })
```

**Important:** Use `make([]adcp.CollectionList, 0)` not `var results []adcp.CollectionList` -- ensures JSON `[]` not `null`.

## Complete Skeleton

```go
package main

import (
    "context"
    "crypto/rand"
    "encoding/hex"
    "fmt"
    "log"
    "strings"
    "sync"
    "time"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

type store struct {
    mu       sync.RWMutex
    lists    map[string]*adcp.CollectionList
    tokens   map[string]string
    resolved map[string][]adcp.ResolvedCollection
}

func uuid() string {
    b := make([]byte, 16)
    rand.Read(b)
    return hex.EncodeToString(b)
}

func generateToken(listID string) string {
    b := make([]byte, 32)
    rand.Read(b)
    return hex.EncodeToString(b)
}

func resolveCollections(list *adcp.CollectionList) []adcp.ResolvedCollection {
    // Replace with real resolution logic.
    // Walk base_collections, look up distribution IDs / publisher collections / genres,
    // apply filters, return matching collections.
    return []adcp.ResolvedCollection{
        {
            Name:          "Example Show",
            CollectionRID: "col-1",
            DistributionIDs: []adcp.DistributionID{
                {Type: "imdb_id", Value: "tt1234567"},
            },
            ContentRating: &adcp.ContentRating{System: "us_tv", Rating: "TV-14"},
            Genre:         []string{"Drama"},
            GenreTaxonomy: "iab_content_3.0",
            Kind:          "series",
        },
    }
}

func createServer(s *store) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-collection-agent", Version: "1.0.0"}, nil)

    // Register all 6 tools using adcp.AddTool:
    // 1. get_adcp_capabilities
    // 2. create_collection_list
    // 3. get_collection_list
    // 4. update_collection_list
    // 5. delete_collection_list
    // 6. list_collection_lists

    return server
}

func main() {
    s := &store{
        lists:    make(map[string]*adcp.CollectionList),
        tokens:   make(map[string]string),
        resolved: make(map[string][]adcp.ResolvedCollection),
    }
    log.Fatal(adcp.Serve(func() *mcp.Server { return createServer(s) }))
}
```

## go.mod

```
module your-collection-agent

go 1.25

require (
    github.com/adcontextprotocol/adcp-go/adcp v0.0.0
    github.com/modelcontextprotocol/go-sdk v1.5.0
)
```

Then `go mod tidy`.

## Key Concepts

### Collection List References

Other agents reference your lists via `CollectionListRef`:

```go
ref := adcp.CollectionListRef{
    AgentURL:  "https://my-collection-agent.example.com/mcp",
    ListID:    "list-abc123",
    AuthToken: "token-from-create-response",
}
```

Sellers embed these in targeting to include/exclude content. The auth token from `create_collection_list` must be stored -- it is only returned once.

### Dynamic Filters

Filters narrow resolved collections without changing the base set:

```go
filters := &adcp.CollectionListFilters{
    ContentRatingsExclude: []adcp.ContentRating{
        {System: "us_tv", Rating: "TV-MA"},
    },
    GenresInclude:  []string{"Drama", "Comedy"},
    GenreTaxonomy:  "iab_content_3.0",
    Kinds:          []string{"series"},
}
```

Include is applied first (allowlist), then exclude narrows further (blocklist).

### Pagination

Collection lists can be large. `get_collection_list` supports pagination with higher limits than standard (max 10,000 per page, default 1,000):

```go
input := adcp.GetCollectionListRequest{
    ListID:     "list-abc123",
    Pagination: &adcp.CollectionPagination{MaxResults: 500},
}
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Missing `auth_token` in create response | Required field -- use `adcp.CreateCollectionListResponse(list, token)` |
| Returning `null` for empty collections | Use `make([]adcp.ResolvedCollection, 0)` or pass `nil` to skip the key |
| Not handling `resolve` default | When `input.Resolve == nil`, default to `true` |
| Patching instead of replacing | `base_collections` and `filters` on update are full replacements |
| Not using constructor functions | Use `adcp.ByDistributionIDs()`, `adcp.ByPublisherCollections()`, `adcp.ByPublisherGenres()` |
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
| `adcp.CapabilitiesResponse(data)` | Response builder |
| `adcp.CreateCollectionListResponse(list, token)` | Response builder |
| `adcp.GetCollectionListResponse(list, collections, pagination)` | Response builder |
| `adcp.UpdateCollectionListResponse(list)` | Response builder |
| `adcp.DeleteCollectionListResponse(listID)` | Response builder |
| `adcp.ListCollectionListsResponse(lists, pagination)` | Response builder |
| `adcp.Result(data, summary)` | Generic response builder |
| `adcp.Error[T](code, opts)` | Error response |

**Constructor functions:** `adcp.ByDistributionIDs(ids)`, `adcp.ByPublisherCollections(domain, ids)`, `adcp.ByPublisherGenres(domain, genres, taxonomy)`

Input types (generated from schemas, also available as `*Input` aliases):

| Tool | Input Type |
|------|-----------|
| `get_adcp_capabilities` | `adcp.EmptyInput` |
| `create_collection_list` | `adcp.CreateCollectionListRequest` |
| `get_collection_list` | `adcp.GetCollectionListRequest` (has `Resolve *bool`, `Pagination`) |
| `update_collection_list` | `adcp.UpdateCollectionListRequest` |
| `delete_collection_list` | `adcp.DeleteCollectionListRequest` |
| `list_collection_lists` | `adcp.ListCollectionListsRequest` |

The skill contains everything you need. Do not read additional docs before writing code.
