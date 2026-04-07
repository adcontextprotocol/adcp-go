# adcp-go

Go SDK and reference implementation for the [Ad Context Protocol (AdCP)](https://adcontextprotocol.org), including the Trusted Match Protocol (TMP) for real-time package activation.

## Packages

| Package | Description |
|---------|-------------|
| `tmproto/` | TMP message types, provider interfaces, JSON codec |
| `targeting/` | Data-driven targeting engine — property bitmaps, freq caps, audiences, intent |
| `targeting/prommetrics/` | Prometheus metrics (stdlib-only, zero external deps) |
| `targeting/valkeystore/` | Valkey/Redis `Store` implementation |
| `router/` | TMP Router — fan-out, merge, privacy enforcement, cached Ed25519 signing |
| `reference/context-agent/` | Reference context match agent — Roaring bitmaps, topic matching |
| `reference/identity-agent/` | Reference identity match agent — frequency capping, audience matching |
| `registry/` | AgenticAdvertising.org registry sync client |
| `tmpclient/` | Publisher-side TMP client library |
| `bench/` | Performance benchmarks — OpenRTB vs TMP JSON |
| `e2e/` | End-to-end tests — multi-agent, chat simulation, frequency capping |

## Documentation

| Document | Description |
|----------|-------------|
| [`docs/network-surface.md`](docs/network-surface.md) | Port map, data flow, TEE pinhole spec, env var reference |
| [`docs/embedding.md`](docs/embedding.md) | Guide for embedding the router in existing systems (e.g., Prebid Server) |
| [`AGENTS.md`](AGENTS.md) | Agent guidelines — hardening priorities, architecture, key files |

## Quick start

```bash
# Build everything (includes tmproto, router, cmd/router)
go build ./...

# Run tests
go test ./...
cd reference/context-agent && go test ./...
cd reference/identity-agent && go test ./...
cd e2e && go test ./...

# Run benchmarks
cd bench && go test -bench=. -benchmem
```

## Performance

Benchmarked on Apple M4 Pro (CPU):

| Operation | ns/op | QPS (single core) |
|-----------|-------|-------------------|
| Roaring bitmap check | 11 ns | 90M |
| Full TMP pipeline | 960 ns | 1.04M |
| OpenRTB equivalent | 2,340 ns | 427K |
| HMAC-SHA256 sign | 142 ns | 7M |
| Ed25519 verify | 30,000 ns | 32K |

TMP JSON full exchange (2 round-trips) is **1.8x faster** than OpenRTB (1 round-trip) with **37% smaller** payloads.

## Architecture

```
Publisher → Router (context path) → Provider A, B, C → merged offers
Publisher → Router (identity path) → Provider A, B, C → merged eligibility
                                                              |
Publisher joins both responses and activates packages ←-------+
```

The router is a single binary with structurally separate code paths for context and identity. Context code never touches identity data; identity code never touches context data.

## License

Apache 2.0
