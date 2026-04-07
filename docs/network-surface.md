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
| `POST` | `/tmp/identity` | Identity match (fans out to identity agents) |
| `POST` | `/tmp/expose` | Exposure recording (fans out to identity agents) |
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
| `POST` | `/tmp/expose` | Record exposure |
| `GET` | `/metrics` | Prometheus metrics |
| `GET` | `/health` | Health check |

## Data Flow

### Context Match

1. Publisher client sends `POST /tmp/context` to router with `property_id`, `placement_id`, `available_packages`, `artifacts`
2. Router enriches request: resolves `property_rid` from registry, computes URL hash, signs with Ed25519
3. Router fans out to matching context agents in parallel (30ms timeout per provider)
4. Each context agent evaluates: property bitmap → suppression → signature → URL filter → topic match
5. Router merges offers and signals from all agents
6. Response to publisher: offers + signals

**Data never in context path:** `user_token`, `uid_type`, `user_id`, `device_id`, `ip_address`

### Identity Match

1. Publisher client sends `POST /tmp/identity` to router with `user_token` (or `identities`), `package_ids`
2. Router fans out to matching identity agents (30ms timeout)
3. Each identity agent evaluates: campaign freq cap → package freq cap → audience → intent score
4. Router merges with AND semantics (eligible only if NO agent says ineligible), intent score = max
5. Response to publisher: per-package eligibility booleans + intent scores

### Exposure Recording

1. Publisher sends `POST /tmp/expose` with `package_id`, `user_token`
2. Router fans out to identity agents
3. Identity agent: reads exposure log from Valkey, appends entry, prunes old entries, writes back
4. Updates frequency sorted sets and intent timestamp

## Pinhole Specification

The identity agent is the privacy boundary. When running in a TEE:

**Enters the TEE:**
- User tokens (hashed internally, never stored raw)
- Package IDs (public)
- Audience segment data (via Valkey replication)
- Frequency cap configurations (via Valkey replication)

**Leaves the TEE (the pinhole):**
- `PackageEligibility` per package: `{package_id, eligible (bool), intent_score (float64)}`
- Exposure response: `{package_id, campaign_count, campaign_remaining}`
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

## Ed25519 Signing

- Router signs context match requests with Ed25519 private key
- Signature cached per `(placement_id, package_set_hash, epoch)`
- Epoch = 60 seconds; signatures valid for current + previous epoch
- Agents verify signatures using property's public key from registry
- Verification can be sampled (0-100% rate)

## Environment Variables

| Variable | Service | Purpose | Default |
|----------|---------|---------|---------|
| `TMP_ROUTER_ADDR` | Router | Listen address | `:8080` |
| `TMP_ROUTER_CONFIG` | Router | Path to JSON config file | (none) |
| `TMP_CONTEXT_ADDR` | Context Agent | Listen address | `:8081` |
| `TMP_CONTEXT_REGISTRY` | Context Agent | Path to registry snapshot | (none) |
| `TMP_IDENTITY_ADDR` | Identity Agent | Listen address | `:8082` |
| `TMP_IDENTITY_REDIS_ADDR` | Identity Agent | Valkey/Redis address | (none, uses in-memory) |

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

Keys stored in Valkey:
- `user:profile:<hash>` — segment memberships (JSON)
- `user:exposures:<hash>` — binary exposure log
- `freq:pkg:<pkg_id>:<hash>` — package frequency sorted set
- `freq:campaign:<campaign_id>:<hash>` — campaign frequency sorted set
- `intent:<pkg_id>:<hash>` — last exposure timestamp
- `audience:<segment>` — set of user hashes in segment
- `topics:package:<pkg_id>` — topic set for package
- `topics:artifact:<url>` — topic set for URL
- `url:blocklist:<pkg_id>` — blocked URL hashes
- `url:allowlist:<pkg_id>` — allowed URL hashes
- `pkg:identity:<pkg_id>` — package identity config (JSON)
- `campaign:freq:<campaign_id>` — campaign frequency config (JSON)
- `pkg:context:<pkg_id>` — package context config (JSON)
