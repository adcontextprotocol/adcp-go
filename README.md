# adcp-go

Go SDK for the [Ad Context Protocol (AdCP)](https://adcontextprotocol.org). Build advertising agents that sell inventory, serve audience data, manage creatives, and pass storyboard compliance validation.

## Building an Agent

The fastest way to build an AdCP agent in Go:

```bash
go get github.com/adcontextprotocol/adcp-go/adcp
```

```go
package main

import (
    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-agent", Version: "1.0.0"}, nil)

    adcp.AddTool(server, "get_adcp_capabilities", "Agent capabilities",
        func(ctx context.Context, req *mcp.CallToolRequest, input adcp.EmptyInput) (*mcp.CallToolResult, any, error) {
            return adcp.CapabilitiesResponse(&adcp.CapabilitiesData{
                SupportedProtocols: []string{"media_buy"},
            })
        })

    // Add more tools...

    log.Fatal(adcp.Serve(func() *mcp.Server { return server }))
}
```

Validate with the storyboard runner:
```bash
go run main.go &
npx @adcp/client storyboard run http://localhost:3001/mcp media_buy_seller --json
```

## Skills

Use skill files to build complete agents with coding assistants (Claude, Codex). Each skill contains domain decisions, tool response shapes, and validation instructions.

| Skill | Agent type | Storyboard | Status |
|-------|-----------|------------|--------|
| [`build-seller-agent`](skills/build-seller-agent/) | Publisher, SSP | `media_buy_seller` | 9/9 validated |
| [`build-signals-agent`](skills/build-signals-agent/) | CDP, data provider | `signal_owned` | 3/4 validated |
| [`build-creative-agent`](skills/build-creative-agent/) | Ad server, CMP | `creative_lifecycle` | In progress |
| [`build-generative-seller-agent`](skills/build-generative-seller-agent/) | AI ad network | `media_buy_generative_seller` | In progress |
| [`build-retail-media-agent`](skills/build-retail-media-agent/) | Retail media | `media_buy_catalog_creative` | In progress |

## SDK Reference

| Function | Usage |
|----------|-------|
| `adcp.AddTool(server, name, desc, handler)` | Register tool with typed input + permissive schema |
| `adcp.Serve(createAgent)` | HTTP server on `:3001/mcp` |
| `adcp.RegisterTestController(server, store)` | Add compliance test controller |
| `adcp.CapabilitiesResponse(data)` | Response builder |
| `adcp.ProductsResponse(data)` | Response builder |
| `adcp.MediaBuyResponse(data)` | Response builder |
| `adcp.DeliveryResponse(data)` | Response builder (dual-key) |
| `adcp.SyncCreativesResponse(results, sandbox)` | Response builder (dual-key) |
| `adcp.Result(data, summary)` | Generic response builder |
| `adcp.Errorf(code, opts)` | Structured error response |

Types: [`adcp/types.go`](adcp/types.go) (hand-written SDK types) + [`adcp/types_gen.go`](adcp/types_gen.go) (generated from [AdCP schemas](https://github.com/adcontextprotocol/adcp), pinned to `v3.0.0-rc.3`)

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
