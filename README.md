# adcp-go

Go SDK for the [Ad Context Protocol (AdCP)](https://adcontextprotocol.org). Build advertising agents that sell inventory, serve audience data, manage creatives, and pass storyboard compliance validation.

## Building an Agent

```bash
go get github.com/adcontextprotocol/adcp-go/adcp
```

```go
package main

import (
    "context"
    "log"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-agent", Version: "1.0.0"}, nil)

    adcp.AddTool(server, "get_adcp_capabilities", "Agent capabilities",
        func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
            return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
                ADCP:               &adcp.ADCPVersion{MajorVersions: []int{3}},
                SupportedProtocols: []string{"media_buy"},
            })
        })

    // Add more tools — see skills/ for complete patterns

    log.Fatal(adcp.Serve(func() *mcp.Server { return server }))
}
```

Validate with the storyboard runner:
```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_seller --json
```

## Skills

Use skill files to build complete agents with coding assistants (Claude, Codex). Each skill is self-contained — a coding agent reads one file and produces a passing agent.

| Skill | Agent type | Storyboard | Status |
|-------|-----------|------------|--------|
| [`build-seller-agent`](skills/build-seller-agent/) | Publisher, SSP | `media_buy_seller` | 9/9 validated |
| [`build-signals-agent`](skills/build-signals-agent/) | CDP, data provider | `signal_owned` | 4/4 validated |
| [`build-creative-agent`](skills/build-creative-agent/) | Ad server, CMP | `creative_lifecycle` | 4/6 validated |
| [`build-generative-seller-agent`](skills/build-generative-seller-agent/) | AI ad network | `media_buy_generative_seller` | 9/9 validated |
| [`build-retail-media-agent`](skills/build-retail-media-agent/) | Retail media | `media_buy_catalog_creative` | 9/9 validated |

## SDK Reference

### Tool registration

```go
adcp.AddTool(server, "tool_name", "Description",
    func(ctx context.Context, req *mcp.CallToolRequest, input InputType) (*mcp.CallToolResult, any, error) {
        return adcp.Result(data, "summary")
    })
```

`AddTool` generates a JSON schema from the Go input struct while allowing additional protocol fields. Use it instead of `mcp.AddTool` (which rejects extra fields).

### Response builders

| Builder | Tool |
|---------|------|
| `adcp.CapabilitiesResponse(data)` | `get_adcp_capabilities` |
| `adcp.ProductsResponse(data)` | `get_products` |
| `adcp.MediaBuyResponse(data)` | `create_media_buy` |
| `adcp.MediaBuysResponse(buys, sandbox)` | `get_media_buys` |
| `adcp.DeliveryResponse(data)` | `get_media_buy_delivery` |
| `adcp.SyncAccountsResponse(accounts, sandbox)` | `sync_accounts` |
| `adcp.GovernanceResponse(accounts)` | `sync_governance` |
| `adcp.SyncCreativesResponse(creatives, sandbox)` | `sync_creatives` |
| `adcp.CreativeFormatsResponse(formats, sandbox)` | `list_creative_formats` |
| `adcp.ListCreativesResponse(items)` | `list_creatives` |
| `adcp.PreviewCreativeResponse(id, name, url, w, h)` | `preview_creative` |
| `adcp.BuildCreativeResponse(manifest, sandbox)` | `build_creative` |
| `adcp.SignalsResponse(signals, sandbox)` | `get_signals` |
| `adcp.ActivateSignalResponse(deployments, sandbox)` | `activate_signal` |
| `adcp.SyncCatalogsResponse(catalogs, sandbox)` | `sync_catalogs` |
| `adcp.SyncEventSourcesResponse(sources, sandbox)` | `sync_event_sources` |
| `adcp.LogEventResponse(received, processed, sandbox)` | `log_event` |
| `adcp.PerformanceFeedbackResponse(sandbox)` | `provide_performance_feedback` |
| `adcp.Result(data, summary)` | Any tool (generic) |
| `adcp.Errorf(code, opts)` | Error response |

### Other

| Function | Usage |
|----------|-------|
| `adcp.Serve(createAgent)` | HTTP server on `:3001/mcp` with timeouts |
| `adcp.RegisterTestController(server, store)` | Compliance test controller (sandbox only) |

### Types

- [`adcp/types.go`](adcp/types.go) — Hand-written SDK types (Product, MediaBuyData, Signal, etc.)
- [`adcp/inputs.go`](adcp/inputs.go) — Typed input structs for all tool handlers
- [`adcp/types_gen.go`](adcp/types_gen.go) — 203 types generated from [AdCP schemas](https://github.com/adcontextprotocol/adcp) v3.0.0-rc.3

## Packages

| Package | Description |
|---------|-------------|
| [`adcp/`](adcp/) | MCP server helpers — `AddTool`, response builders, test controller, `Serve()` |
| [`skills/`](skills/) | SKILL.md files for coding agent generation |
| [`tmproto/`](tmproto/) | TMP message types, provider interfaces, JSON codec |
| [`targeting/`](targeting/) | Targeting engine — property bitmaps, freq caps, audiences, intent |
| [`router/`](router/) | TMP Router — fan-out, merge, privacy enforcement, Ed25519 signing |
| [`registry/`](registry/) | AgenticAdvertising.org registry sync client |
| [`tmpclient/`](tmpclient/) | Publisher-side TMP client library |

## TMP Performance

Benchmarked on Apple M4 Pro:

| Operation | ns/op | QPS (single core) |
|-----------|-------|-------------------|
| Full TMP pipeline | 960 ns | 1.04M |
| OpenRTB equivalent | 2,340 ns | 427K |

TMP is **1.8x faster** than OpenRTB with **37% smaller** payloads.

## Documentation

| Document | Description |
|----------|-------------|
| [`AGENTS.md`](AGENTS.md) | Agent guidelines — hardening, architecture, key files |
| [`docs/network-surface.md`](docs/network-surface.md) | Port map, data flow, TEE pinhole spec |
| [`docs/embedding.md`](docs/embedding.md) | Embedding the router in host systems (e.g., Prebid Server) |

## License

Apache 2.0
