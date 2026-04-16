# AGENTS.md

## Project purpose

adcp-go is the Go SDK and reference implementation for the Ad Context Protocol (AdCP) Trusted Match Protocol (TMP). It provides a router, targeting engine, and reference agents for real-time ad package activation with structural privacy guarantees.

## Production hardening is a first-class priority

This project is designed to run in trusted execution environments (TEEs) and be embedded into existing ad tech infrastructure (e.g., Prebid Server). Every change should consider:

- **Zero unnecessary dependencies.** The root module has no external deps. Sub-modules should minimize their dependency tree. No protobuf, no /proc readers, no heavy frameworks in TEE-bound code.
- **The pinhole is sacred.** The identity agent runs in a TEE. Only eligibility booleans, intent scores, and aggregate metrics leave. No user tokens, segments, exposure logs, or frequency counts. Every new field on a response type is a potential pinhole violation — review carefully.
- **Metrics must not leak data.** No user-controlled values (package IDs, tokens, URLs) in Prometheus label values. Labels must be bounded (stage names, boolean pass/fail). Unbounded labels are a cardinality bomb and a DoS vector.
- **Error messages must be generic.** Internal errors return `"internal error"` to callers. Details go to structured logs (slog) inside the service boundary. Never echo `err.Error()` in HTTP responses. Never interpolate user-supplied values into error messages. Pattern: `slog.Error("description", "error", err)` server-side, `Message: "internal error"` in the HTTP response.
- **The router is a library.** It should be embeddable in host applications. Use `RouterOption` functional options for injection points (`WithHTTPClient`, `WithLogger`). Don't use global state. Don't call `slog.SetDefault()` in library code.
- **Config is layered.** Flags > env vars > JSON config > defaults. All services follow this pattern. Env vars use `TMP_` prefix.

## Architecture

```
Publisher -> Router (:8080)  -> Context Agent (:8081)   [property bitmaps, topics, URLs]
                             -> Identity Agent (:8082)  [freq caps, audiences, intent]
                                    |
                                 Valkey (localhost:6379, inside TEE)
```

The targeting engine (`targeting/`) is the shared evaluation core. Reference agents are thin HTTP shims over it. The engine uses a `Store` interface (Valkey or in-memory) and a `Metrics` interface (Prometheus or noop).

## Key files

| File | Role |
|------|------|
| `targeting/metrics.go` | `Metrics` interface — the observability hook. Zero deps. |
| `targeting/engine.go` | Evaluation pipeline — all targeting logic lives here. |
| `targeting/store.go` | `Store` interface — abstracts Valkey. |
| `targeting/prommetrics/` | Stdlib-only Prometheus text format implementation. |
| `router/router.go` | Fan-out, merge, signing, circuit breaker. Embeddable via `RouterOption`. |
| `router/serverconfig.go` | Config loading (JSON file, env vars, defaults). |
| `cmd/router/main.go` | Router binary entry point — wires components, Prometheus metrics, env vars. |
| `docs/network-surface.md` | Port map, data flow, pinhole spec, env var reference. |
| `docs/embedding.md` | Guide for embedding the router in host applications. |
| `adcp/serve.go` | One-liner HTTP server for AdCP MCP agents. |
| `adcp/responses.go` | Response builders — `CapabilitiesResponse`, `ProductsResponse`, etc. |
| `adcp/errors.go` | Structured AdCP error builder. |
| `adcp/types.go` | AdCP domain types — products, media buys, signals, creatives, collections, business terms. |
| `adcp/testcontroller.go` | `RegisterTestController` — comply_test_controller for storyboard testing. |
| `skills/` | SKILL.md files for coding agents to generate AdCP agents (seller, signals, creative, generative-seller, retail-media, collection). |

## Testing

```bash
# Root module (router, targeting, tmproto, tmpclient)
go test ./...

# Sub-modules
cd cmd/router && go test ./...
cd targeting/prommetrics && go test ./...
cd reference/context-agent && go test ./...
cd reference/identity-agent && go test ./...
cd e2e && go test ./...
```

## Module structure

The workspace uses Go multi-module layout. The root module has zero external dependencies. Sub-modules add deps only where needed:

- `targeting/prommetrics/` — stdlib only (no Prometheus client library)
- `targeting/valkeystore/` — `go-redis/v9`
- `reference/context-agent/` — `RoaringBitmap/roaring`
- `reference/identity-agent/` — `go-redis/v9`, `prommetrics`
- `cmd/router/` — `prommetrics`
