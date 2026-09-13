# adcp-go

Go SDK for the [Ad Context Protocol (AdCP)](https://adcontextprotocol.org). Build advertising agents that sell inventory, serve audience data, manage creatives, and pass storyboard compliance validation.

## Local development

This repo is a multi-module Go workspace. To work on multiple modules at once with unpublished changes visible across them, copy `go.work.example` to `go.work`:

```sh
cp go.work.example go.work
go build ./...
```

`go.work` is intentionally gitignored so CI and downstream consumers resolve real tagged versions of each sub-module, while local development uses your working tree directly.

## Modules & versioning

This repository is a Go multi-module workspace. The top-level module
(`github.com/adcontextprotocol/adcp-go`) is internal glue — test helpers,
workspace scaffolding, and cross-module examples — and is not intended for
external import. The published API surface is delivered through the sub-modules
below, each with its own tag prefix and SemVer cadence.

| Import path | Current version | Tag format |
| --- | --- | --- |
| `github.com/adcontextprotocol/adcp-go/adcp/v3` | `v3.0.0` | `adcp/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/adcp` (frozen v2) | `v2.1.1` | `adcp-vX.Y.Z` (legacy hyphen) |
| `github.com/adcontextprotocol/adcp-go/tmproto` | `v0.1.0` | `tmproto/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/tmpclient` | `v0.1.0` | `tmpclient/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/targeting` | `v0.1.0` | `targeting/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/urlcanon` | `v0.1.0` | `urlcanon/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry` | `v0.1.0` | `registry/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry/redisstore` | (not yet cut) | `registry/redisstore/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry/glidestore` | (not yet cut) | `registry/glidestore/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/adcp/v3/signing/awskms` | (not yet cut) | `adcp/v3/signing/awskms/vX.Y.Z` |

The `adcp/vN` major version tracks the AdCP protocol spec's major version 1:1.
`adcp/v3` speaks AdCP 3.x; a hypothetical future `adcp/v4` will ship when the
protocol spec bumps to 4.0. Other sub-modules stay on independent SemVer.

Upgrading from `adcp` v2 or an earlier root-module pseudo-version? See
[`MIGRATING.md`](MIGRATING.md) for the full recipe — import path map, sample
`go.mod` diffs, rollback steps, and the tag-naming history behind the v2
module.

## Building an Agent

```bash
go get github.com/adcontextprotocol/adcp-go/adcp
```

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/adcontextprotocol/adcp-go/adcp"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
    server := mcp.NewServer(&mcp.Implementation{Name: "my-agent", Version: "1.0.0"}, nil)

    adcp.Register(server, adcp.Config{
        IdempotencyReplayTTL: 24 * time.Hour,
        Capabilities: &adcp.CapabilitiesData{
            SupportedProtocols: []string{"media_buy"},
            MediaBuy: &adcp.MediaBuyCapabilities{
                SupportedPricingModels: []string{"cpm"},
            },
        },
        GetProducts: func(ctx context.Context, acct any, req *adcp.GetProductsRequest) (*adcp.ProductsData, error) {
            return &adcp.ProductsData{Products: []adcp.Product{}}, nil
        },
    })

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
| [`build-creative-agent`](skills/build-creative-agent/) | Ad server, CMP | `creative_lifecycle` | 6/6 validated |
| [`build-generative-seller-agent`](skills/build-generative-seller-agent/) | AI ad network | `media_buy_generative_seller` | 9/9 validated |
| [`build-retail-media-agent`](skills/build-retail-media-agent/) | Retail media | `media_buy_catalog_creative` | 9/9 validated |

## SDK Reference

### Tool registration

```go
adcp.AddTool(server, "get_products", "Available advertising products",
    func(ctx context.Context, req *mcp.CallToolRequest, input adcp.GetProductsRequest) (*mcp.CallToolResult, any, error) {
        return adcp.ProductsResponse(&adcp.ProductsData{Products: []adcp.Product{}})
    })
```

`AddTool` generates a JSON schema from the Go input struct while allowing additional protocol fields. Use it instead of `mcp.AddTool` (which rejects extra fields).

Prefer `adcp.Register` for seller agents when possible. It wires common tools,
fills required idempotency and capability fields, and handles AdCP 3.0/3.1
capability negotiation. See [`docs/adcp-version-compatibility.md`](docs/adcp-version-compatibility.md)
for the version compatibility contract.

### Response builders

| Builder | Tool |
|---------|------|
| `adcp.CapabilitiesResponse(data)` | `get_adcp_capabilities` |
| `adcp.ProductsResponse(data)` | `get_products` |
| `adcp.MediaBuyResponse(*CreateMediaBuySuccess\|*CreateMediaBuyError\|*CreateMediaBuySubmitted)` | `create_media_buy` |
| `adcp.CreateMediaBuySuccessResponse(data)` | synchronous `create_media_buy` |
| `adcp.CreateMediaBuyErrorResponse(data)` | schema error branch for `create_media_buy` |
| `adcp.CreateMediaBuySubmittedResponse(taskID, message)` | async `create_media_buy` |
| `adcp.MediaBuysResponse(buys, sandbox)` | `get_media_buys` |
| `adcp.MediaBuysDataResponse(data)` | `get_media_buys` with pagination/errors |
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
| `adcp.LogEventResponse(received, processed, matchQuality, sandbox)` | `log_event` |
| `adcp.PerformanceFeedbackResponse(sandbox)` | `provide_performance_feedback` |
| `adcp.Errorf(code, opts)` | Error response |

### Other

| Function | Usage |
|----------|-------|
| `adcp.Serve(createAgent)` | HTTP server on `:3001/mcp` with timeouts |
| `adcp.RegisterTestController(server, store)` | Compliance test controller (sandbox only) |

### Types

- [`adcp/types.go`](adcp/types.go) — Hand-written SDK types (Product, MediaBuyData, Signal, etc.)
- [`adcp/inputs.go`](adcp/inputs.go) — Typed input structs for all tool handlers
- [`adcp/types_gen.go`](adcp/types_gen.go) — types generated from [AdCP schemas](https://github.com/adcontextprotocol/adcp) 3.1.0
- [`adcp/governance_types.go`](adcp/governance_types.go) — hand-written `Plan` and related governance types (inline in sync_plans)
- [`adcp/plan_validate.go`](adcp/plan_validate.go) — client-side enforcement of the budget `oneOf` and Annex III `if/then` invariants

Media-buy seller helpers use the schema variants directly:
`*CreateMediaBuySuccess`, `*CreateMediaBuyError`, or `*CreateMediaBuySubmitted`
for `create_media_buy`, and `MediaBuyData` for items returned by
`get_media_buys`. `MediaBuyData.Packages` is `[]PackageStatus`; create success
packages remain `[]Package`.
Use `adcp.Bool(true)` for optional boolean updates and `adcp.Float64(0)` when a
creative assignment weight of zero is intentional. `CreativeAssignment.Extra`
preserves seller-specific assignment fields allowed by the schema.

Generated enum aliases decode unknown wire values for forward compatibility.
When handler logic needs strict current-schema validation, use the generated
helpers: `KnownMediaBuyStatusValues()`, `IsKnownMediaBuyStatus(status)`, and
`ParseMediaBuyStatus(raw)`.

For typed structs whose schema-required fields cannot be represented by Go's
zero values, use the SDK validators before request submission or handler-side
acceptance. Validation is opt-in and intentionally narrower than full JSON
Schema validation: it catches required fields and schema invariants the Go type
cannot express, but keeps unknown enum and oneOf variant values
forward-compatible unless `adcp.WithStrictEnums()` is supplied.
Flattened oneOf types are validated by active branch: event-only fields such as
`attribution_window` are checked on event goals and ignored on metric goals.
For value-based event targets such as `per_ad_spend` and `maximize_value`, the
validator also requires at least one event source `value_field`; that is an SDK
invariant derived from the schema descriptions, not a general-purpose JSON
Schema validation pass.

```go
goal := adcp.OptimizationGoal{
    Kind:   "event",
    Target: adcp.OptimizationGoalCostPerTarget{Value: 10},
    EventSources: []adcp.OptimizationGoalEventSource{{
        EventSourceID: "pixel-1",
        EventType:     "purchase",
    }},
}

if issues := goal.Validate(); len(issues) > 0 {
    // Map issue.Code and issue.Field to your request error handling.
}
```

## Request signing (AdCP 3.0 optional, 4.0 required)

`adcp/signing` implements the AdCP RFC 9421 request-signing profile — optional in 3.0, required for spend-committing operations in 4.0. The package is self-validating against the spec's [conformance vectors](https://adcontextprotocol.org/compliance/latest/test-vectors/request-signing/): all 8 positive + 20 negative vectors pass, and signed Ed25519 signatures match the committed positive-vector bytes.

**As an agent that signs requests:**

```go
import "github.com/adcontextprotocol/adcp-go/adcp/signing"

pemBytes, _ := os.ReadFile("signing.pem")
priv, _, _ := signing.LoadPrivateKey(pemBytes)
signer, _ := signing.NewSigner(signing.SignerOptions{
    KeyID:      "buyer-ed25519-2026",
    PrivateKey: priv,
})
client := &http.Client{Transport: signer.RoundTripper(nil, true /* cover content-digest */)}
resp, err := client.Post("https://seller.example.com/adcp/create_media_buy", "application/json", body)
```

**As a verifier (seller):**

```go
mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver:            signing.NewHTTPJWKSResolver(agents), // SSRF-safe fetcher
    Replay:              signing.NewMemoryReplayStore(0),
    Revocation:          signing.NewStaticRevocationList(nil),
    OperationResolver:   signing.DefaultOperationResolver,    // /adcp/<op>
    RequiredFor:         []string{"create_media_buy"},
    ContentDigestPolicy: signing.DigestRequired,
})
http.ListenAndServe(":8080", mw(yourHandler))
```

**Generate a signing keypair:**

```bash
go run github.com/adcontextprotocol/adcp-go/adcp/cmd/adcp-signing-keygen -alg ed25519 -kid my-agent-2026 -out signing.pem
```

Emits a PEM-encoded private key plus the public JWK (with `adcp_use: "request-signing"`) ready to paste into your agent's JWKS document at `jwks_uri`.

## Packages

| Package | Description |
|---------|-------------|
| [`adcp/`](adcp/) | MCP server helpers — `AddTool`, response builders, test controller, `Serve()` |
| [`adcp/signing/`](adcp/signing/) | RFC 9421 request signing — signer, verifier, middleware, conformance tests |
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

## Contributing

Contributions welcome! All contributors must agree to the [AgenticAdvertising.Org IPR Policy](https://github.com/adcontextprotocol/adcp/blob/main/IPR_POLICY.md) — the bot prompts new contributors on their first PR and a single signature covers all AAO repositories.

## License

Apache 2.0
