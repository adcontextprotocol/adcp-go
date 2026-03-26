# adcp-go

Go SDK and reference implementation for the [Ad Context Protocol (AdCP)](https://adcontextprotocol.org), including the Trusted Match Protocol (TMP) for real-time package activation.

## Packages

| Package | Description |
|---------|-------------|
| `tmp/` | TMP message types, provider interfaces, JSON codec |
| `router/` | TMP Router — fan-out, merge, privacy enforcement, cached Ed25519 signing |
| `reference/context-agent/` | Reference context match agent — Roaring bitmaps, modular pipeline, Valkey |
| `reference/identity-agent/` | Reference identity match agent — frequency capping, audience matching, expose endpoint |
| `bench/` | Performance benchmarks — OpenRTB vs TMP JSON |
| `e2e/` | End-to-end tests — multi-agent, chat simulation, frequency capping |

## Quick start

```bash
# Build everything
go build ./...

# Run tests
cd router && go test ./...
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
