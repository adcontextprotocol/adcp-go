# Network Surface

## Architecture

```
                          ┌─────────────────────────────────────────────┐
                          │              Trust Boundary (TEE)           │
                          │                                             │
Publisher ──► Router ─────┤──► Identity Agent (:8082)                   │
  Client      (:8080)     │        │                                    │
                │         │        ▼                                    │
                │         │   Valkey (127.0.0.1:6379)                   │
                │         │   (user tokens, segments, exposure logs,    │
                │         │    frequency sets, audience sets)           │
                │         └─────────────────────────────────────────────┘
                │
                └────────► Context Agent (:8081)
                               │
                               ▼
                          In-memory store
                          (property bitmaps, topic sets)


AgenticAdvertising.org ◄── Registry Syncer (outbound HTTPS polling)
```

## Port Map

| Service | Default Port | Protocol | Direction | Configurable Via |
|---------|-------------|----------|-----------|------------------|
| Router | `:8080` | HTTP | Inbound (publishers) | `--addr` / `TMP_ROUTER_ADDR` |
| Context Agent | `:8081` | HTTP | Inbound (router) | `--addr` / `TMP_CONTEXT_ADDR` |
| Identity Agent | `:8082` | HTTP | Inbound (router) | `--addr` / `TMP_IDENTITY_ADDR` |
| Valkey | `localhost:6379` | Redis protocol | Local only | `--redis-addr` / `TMP_IDENTITY_REDIS_ADDR` |
| Registry API | Remote | HTTPS | Outbound | `registry.NewClient(baseURL, token)` |

## HTTP Endpoints

### Router

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/tmp/context` | Context match (fans out to context agents) |
| `POST` | `/tmp/identity` | Identity match (fans out to identity agents, returns TMPX tokens) |
| `GET` | `/registry/snapshot` | Registry snapshot for agents |
| `GET` | `/metrics` | Prometheus metrics |
| `GET` | `/health` | Health check (returns version) |

### Context Agent

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/tmp/context` | Evaluate context match |
| `GET` | `/metrics` | Prometheus metrics |
| `GET` | `/health` | Health check |

### Identity Agent

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/tmp/identity` | Evaluate identity match |
| `GET` | `/metrics` | Prometheus metrics |
| `GET` | `/health` | Health check |

## Data Flow

### Context Match

1. Publisher client sends `POST /tmp/context` to router with `property_id`, `placement_id`, `available_packages`, `artifacts`
2. Router enriches request: resolves `property_rid` from registry, computes URL hash, signs per provider with Ed25519 (`X-AdCP-Signature` / `X-AdCP-Key-Id`)
3. Router fans out to matching context agents in parallel (30ms timeout per provider). Signature is reused across requests for the same `(placement_id, provider, epoch)` from the in-process cache.
4. Each context agent verifies the signature against the router's published key, then evaluates: property bitmap → suppression → URL filter → topic match
5. Router merges offers and signals from all agents
6. Response to publisher: offers + signals

**Data never in context path:** `user_token`, `uid_type`, `user_id`, `device_id`, `ip_address`

### Identity Match

1. Publisher client sends `POST /tmp/identity` to router with `user_token` (or `identities`), `package_ids`, `country`
2. Router filters providers by `country` and `uid_type`, strips `country` before forwarding (the country is not part of the signing input)
3. Router signs per provider with Ed25519 — each signature binds to the provider's registered endpoint URL (a signature minted for provider A is rejected by provider B)
4. Router fans out to matching identity agents (30ms timeout)
5. Each identity agent verifies the signature, then evaluates: campaign freq cap → package freq cap → audience → intent score, returns TMPX token
6. Router merges eligible package lists (union — packages are provider-specific)
7. Router collects TMPX tokens into `tmpx_providers` map keyed by provider ID
8. Response to publisher: eligible package ID list + TTL + provider-keyed TMPX tokens

### Exposure Tracking (TMPX)

Exposure tracking uses encrypted TMPX tokens instead of a dedicated endpoint:

1. Identity agent generates an HPKE-encrypted TMPX token containing resolved user identity tokens
2. Token flows through the router to the publisher as an opaque string
3. Publisher substitutes provider-specific TMPX values into creative tracking URLs (e.g., `{TMPX_S3}`)
4. Buyer's impression pixel receives the token, decrypts it, and updates per-user frequency state

## Pinhole Specification

The identity agent is the privacy boundary. When running in a TEE:

**Enters the TEE:**
- User tokens (hashed internally, never stored raw)
- Package IDs (public)
- Audience segment data (via Valkey replication)
- Frequency cap configurations (via Valkey replication)

**Leaves the TEE (the pinhole):**
- `eligible_package_ids` ([]string) — package IDs the user is eligible for
- `ttl_sec` (int) — caching duration
- `tmpx` (string) — HPKE-encrypted exposure token (opaque, only buyer's cluster master can decrypt)
- Prometheus metrics (counters and histograms, no user data)
- Health check responses

**Never leaves the TEE:**
- Raw user tokens
- Hashed user tokens
- Segment memberships
- Exposure log entries
- Frequency cap counts per user
- Valkey data

## Request Limits

| Limit | Value |
|-------|-------|
| Max request body | 64 KB |
| Max response body (from provider) | 1 MB |
| Provider timeout (default) | 30 ms |
| HTTP read timeout | 5 s |
| HTTP write timeout | 10 s |
| HTTP idle timeout | 120 s |
| Redis pool size | 20 |
| Redis dial timeout | 2 s |
| Redis read/write timeout | 1 s |

## Circuit Breaker

The router tracks per-provider health:
- Opens after N consecutive failures (default: 3)
- Cooldown before half-open probe (default: 10s)
- Timeout and error both count as failures
- Success resets consecutive failure counter

## Request Authentication (Ed25519)

The router signs every outbound `/tmp/context` and `/tmp/identity` request per the [TMP spec](https://adcontextprotocol.org/docs/trusted-match/specification#request-authentication). Providers verify the signature against the router's published public key (discovered via the registry) before evaluating the request.

**Headers attached to every fan-out:**

| Header | Value |
|---|---|
| `X-AdCP-Signature` | Ed25519 signature, base64url, no padding |
| `X-AdCP-Key-Id` | Key identifier (`kid`) used to sign |

**Signed inputs:**

- **Context match** — newline-joined: `context_match_request | property_rid | placement_id | sorted-comma-joined package_ids | provider_endpoint_url | daily_epoch`. Cached on the router per `(placement_id, provider_endpoint_url, epoch)` — context-match signing inputs are static across requests within an epoch.
- **Identity match** — `hex(SHA-256(JCS({type, request_id, identities_hash, consent, package_ids, provider_endpoint_url, daily_epoch})))`. Per-request, never cached. RFC 8785 JCS protects against delimiter-injection from arbitrary-byte fields like `consent.gpp`.

**Replay window:** `daily_epoch = floor(unix_timestamp / 86400)`. Verifiers accept signatures bound to current or previous epoch (~48h). Stale epochs are rejected.

**Per-provider binding:** every signature includes the registered `provider_endpoint_url`. A signature minted for provider A is rejected by provider B even with an identical body.

**Key distribution:** the router's public key is published as a `signing_keys` JWK on the property records served by `GET /registry/snapshot`. Reference providers poll the snapshot URL on a 5-minute interval (`tmproto.RemoteKeyStore`) and look up by `kid`. Revocation: set `revoked_at` on the JWK; verifiers reject signatures whose epoch is at or after the revocation timestamp.

**Configuration:**

- Router: `TMP_ROUTER_SIGNING_KID`, `TMP_ROUTER_SIGNING_KEY_PATH` (PEM PKCS#8 Ed25519), `TMP_ROUTER_SIGNING_PROPERTY_RIDS` (comma-separated RIDs the router is authorized to sign for). Set `TMP_ROUTER_SIGNING_DISABLED=true` to opt out (dev only).
- Reference agents: `--registry-url` (default off — accepts unsigned), `--require-signature`, `--own-endpoint-url`. Env equivalents: `TMP_{IDENTITY,CONTEXT}_REGISTRY_URL`, `TMP_{IDENTITY,CONTEXT}_REQUIRE_SIGNATURE`, `TMP_{IDENTITY,CONTEXT}_ENDPOINT_URL`.

## Environment Variables

| Variable | Service | Purpose | Default |
|----------|---------|---------|---------|
| `TMP_ROUTER_ADDR` | Router | Listen address | `:8080` |
| `TMP_ROUTER_CONFIG` | Router | Path to JSON config file | (none) |
| `TMP_ROUTER_SIGNING_KID` | Router | Key identifier for outbound signatures | (none) |
| `TMP_ROUTER_SIGNING_KEY_PATH` | Router | PEM PKCS#8 Ed25519 private key path | (none) |
| `TMP_ROUTER_SIGNING_PROPERTY_RIDS` | Router | Comma-separated property RIDs the router signs for | (none) |
| `TMP_ROUTER_SIGNING_DISABLED` | Router | Disable request signing (dev only — fail-closed otherwise) | `false` |
| `TMP_CONTEXT_ADDR` | Context Agent | Listen address | `:8081` |
| `TMP_CONTEXT_REGISTRY` | Context Agent | Path to local registry snapshot | (none) |
| `TMP_CONTEXT_REGISTRY_URL` | Context Agent | URL of router's `/registry/snapshot` for signing keys | (none) |
| `TMP_CONTEXT_ENDPOINT_URL` | Context Agent | Own registered endpoint URL (signed-binding check) | (none) |
| `TMP_CONTEXT_REQUIRE_SIGNATURE` | Context Agent | Reject unsigned requests | `false` |
| `TMP_IDENTITY_ADDR` | Identity Agent | Listen address | `:8082` |
| `TMP_IDENTITY_REDIS_ADDR` | Identity Agent | Valkey/Redis address | (none, uses in-memory) |
| `TMP_IDENTITY_REGISTRY_URL` | Identity Agent | URL of router's `/registry/snapshot` for signing keys | (none) |
| `TMP_IDENTITY_ENDPOINT_URL` | Identity Agent | Own registered endpoint URL (signed-binding check) | (none) |
| `TMP_IDENTITY_REQUIRE_SIGNATURE` | Identity Agent | Reject unsigned requests | `false` |

All services also accept `--addr` and other flags. Flags take precedence over environment variables.

## Configuration Precedence

```
Flags > Environment Variables > JSON Config File > Defaults
```

## Valkey in TEE

When running in a TEE, Valkey runs as a local sidecar inside the enclave:
- Bound to `127.0.0.1:6379` (not network-accessible)
- No disk persistence (`save ""`)
- Data enters via Redis replication over vsock from external primary
- All data encrypted at rest outside the TEE; decrypted inside

Data categories stored in Valkey:
- User profiles (segment memberships)
- Exposure logs (binary format)
- Frequency cap counters (package and campaign level)
- Intent timestamps
- Audience sets
- Topic and URL indexes
- Package and campaign configuration
