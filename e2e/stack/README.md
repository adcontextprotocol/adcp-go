# Containerized end-to-end stack

Runs the three published service images — `identity-agent`, `context-agent`
and the TMP `router` — as a single wired deployment against a real Valkey and
stubs for the two external systems they depend on, then drives the router's
public protocol surface and asserts the outcome of every hop.

```sh
./run.sh                     # build the three images from this working copy
./run.sh --published         # pull the published images at :edge
./run.sh --published v1.2.3  # pull the published images at a given tag
```

Exit status is the verdict. `KEEP_STACK=1 ./run.sh` leaves the stack running so
you can poke at it.

## What it proves

An offer coming back from the router is only possible if every hop worked, so
the assertions are deliberately end-to-end rather than per-component:

| Hop | How a break surfaces |
| --- | --- |
| registry feed → router | `/registry/snapshot` is missing a property |
| router signing key → snapshot | no `signing_keys` entry with the router's kid |
| router → provider dial | every context and identity check fails |
| router signature → agent verification | agents answer 401, offers come back empty |
| registry authorization rows → context-agent keystore | context offers come back empty |
| registry feed → context-agent property bitmap | every request short-circuits |
| identity-config stub → identity-agent | packages resolve to nothing |
| seeded Valkey → agent reads | the wrong offers or the wrong eligibility |
| provider responses → router merge | offers or `eligible_package_ids` differ |

Six groups of checks; the verifier prints the total and the verdict:

**router surface.** `/registry/snapshot` carries every property the feed
published, with its domain, plus the router's own signing key. `/providers`
lists both providers. Each stub's call counter is positive, which rules out a
run where an agent never reached the external system its stub stands for and
the offers came from something baked into an image instead.

**context match.** Seven requests isolate one gate apiece: topic overlap (matched artifact vs. non-overlapping artifact), context
signal (seeded key vs. absent key), per-package property scoping, the
property-level suppression kill switch, an unregistered `property_rid`, and
`property_id`-only enrichment where the router has to resolve the RID. Each
offer's `summary` is checked against the seeded package config, so the offer
body is proven to have travelled from Valkey rather than being synthesized. The
two scenarios that expect *no* offers additionally assert the provider was
dialed, because an empty offer list is equally what the router returns when it
excluded the provider from the fan-out entirely. Those two are isolated from
each other by `property_rid`; the rest get a placement apiece (see the cache
note below).

**identity match.** The same three packages resolve differently for two users:
one is in the audience segment and carries a frequency-cap marker, the other
neither. A third request omits `package_ids` so the agent has to resolve the
seller's package set out of the config snapshot.

**router context cache.** Two identical requests on a placement nothing else
touches produce one cache miss and one cache hit. Both are asserted against the
expected three-package offer set rather than merely against each other — the
router caches empty responses too, so an agent returning nothing would satisfy a
first-equals-second comparison and still move both counters.

**signature enforcement.** Unsigned requests sent directly to each agent —
bypassing the router — must be rejected with 401. Without this, every check
above would also pass on a stack where signature verification was off.

**router metrics.** Request, offer and per-provider duration series are
present and positive; the provider timeout and error counters do not move
during the asserted scenarios; both provider health gauges read 1.

## The stack

```
                 ┌──────────────┐
   verify ──────▶│    router    │  /tmp/context, /tmp/identity
                 │              │  /registry/snapshot, /healthz
                 └──┬────┬───┬──┘  /metrics, /providers (admin listener)
      registry feed │    │   │ signed fan-out
        ┌───────────┘    │   └──────────────┬─────────────────┐
        ▼                │                  ▼                 ▼
 ┌──────────────┐        │         ┌────────────────┐ ┌────────────────┐
 │ registrystub │        │         │ context-agent  │ │ identity-agent │
 │ /api/registry│◀───────┼─────────┤ registry feed  │ │                │
 │   /feed      │        │         │ + key lookup   │ └───┬────────┬───┘
 │   /authoriz… │◀───────┼─────────┴───────┬────────┘     │        │
 └──────────────┘        └── key lookup ───┼──────────────┘        │
                                                 ┌─────────┘        │
                                                 ▼                  ▼
                                         ┌──────────────┐  ┌──────────────┐
                                         │    valkey    │  │  configstub  │
                                         │ db0 context  │  │ /v1/identity-│
                                         │ db1 audience │  │    configs   │
                                         │ db2 fcap     │  └──────────────┘
                                         └──────────────┘
```

Five programs, all in one tools image:

- **`bootstrap`** generates the router's Ed25519 signing key (PKCS#8 PEM), the
  public JWK the registry stub serves, and the router's config file — provider
  list, health, cache, signing and registry-feed settings, all derived from
  `internal/fixture`. Generating the config rather than committing it is what
  makes it impossible for the router's view of the stack to drift from what the
  seeder wrote and the verifier asserts.
- **`registrystub`** serves the AdCP registry data endpoints: the cursor-paged
  change feed (`/api/registry/feed`) that both the router and the context-agent
  sync, and the per-agent authorization rows (`/api/registry/authorizations`)
  an agent reads under `TMP_REGISTRY_MODE=authorization`.
- **`configstub`** serves the identity-config source the identity-agent polls
  at `CONFIG_SOURCE_URL`.
- **`seed`** populates the three logical Valkey databases through the same
  store packages the agents read through — `mediabuystore`, `pkgconfigstore`,
  `topicstore`, `signalstore`, `suppressionstore`, `audience`, `fcap` — so a
  key layout is never hand-written here. See the note on module resolution
  below for the limit of that guarantee.
- **`verify`** drives the router and prints the verdict.

`internal/fixture` is the single source of truth for every identifier the
processes share. Nothing there is read from the environment.

## Startup ordering

Every constraint compose can express is declared with `depends_on` in the compose
file. One cannot be — step 4 below, a readiness condition on a sibling's response
body — so a bare `docker compose up -d` brings up everything except the
identity-agent, which exits 1 naming the snapshot URL it could not reach.
`run.sh` is what sequences that properly:

1. **valkey** is seeded before **context-agent** starts, because the agent
   loads its suppression snapshot at boot.
2. **bootstrap** runs before **router** (which needs the config and the key)
   and before **registrystub** (which serves the public JWK). It reuses an
   existing key rather than minting a new one, because it is a dependency of
   both and a second run that replaced the key would leave the stub publishing a
   JWK the router no longer signs with.
3. **registrystub** serves the feed before **router** can merge it into
   `/registry/snapshot`, and serves the authorization rows before
   **context-agent** can resolve a signing key from them.
4. **router** publishes its signing key on `/registry/snapshot` before
   **identity-agent** starts. That agent's snapshot keystore does one
   synchronous fetch at boot and refuses traffic without a key, so this is a
   hard sequence rather than a preference.

The verifier additionally waits for the context-agent's property bitmap to
finish hydrating from the feed, on a fresh placement per attempt so the
empty-offer response it may cache along the way affects neither an assertion
nor the rest of its own readiness budget.

## Both keystore modes, one run

The two agents resolve the router's signing key differently on purpose:

- **context-agent** runs `TMP_REGISTRY_MODE=authorization` against the registry
  stub's `/api/registry/authorizations`, resolving a key per request from the
  authorization rows for the request's `seller_agent_url`. This is the mode a
  deployment sitting behind the public registry runs.
- **identity-agent** runs `TMP_REGISTRY_MODE=snapshot` against the router's own
  `/registry/snapshot`, which the router populates by merging the registry feed
  with its own public key.

A single run therefore covers both key-distribution paths, and a break in
either one shows up as empty results from exactly one agent.

## Things worth knowing before you change it

**The network is addressed out of `100.64.0.0/24`, not a Docker default.**
The router refuses to dial a provider whose hostname resolves into loopback,
link-local or an RFC 1918 range — the DNS-rebinding guard on its fan-out and
health-check clients. Every Docker default bridge pool is RFC 1918, so on a
default network the router cannot reach a sibling container and every fan-out
fails closed. RFC 6598 shared address space is not RFC 1918, so the guard stays
fully active and the resolved address passes it: the stack exercises the real
dial path rather than an SSRF bypass. Override with `E2E_SUBNET` if the range
collides with something on your host.

**Valkey must be 9.0 or newer.** The frequency-cap store writes markers with
`HSETEX`, which older servers do not implement.

**The three services under test carry `restart: "no"`.** A restart would reset
the router's metrics counters, which the verifier reads as deltas to prove no
provider fault occurred, and would re-warm an agent — so a crash-loop would
read as a clean run.

**Each context scenario gets its own placement, suffixed with a per-run
nonce.** The router keys cached provider responses on `{property_rid,
placement_id, provider_id, seller_agent_url, country}`. `artifact_refs` is not
part of that key, so two requests differing only by artifact share one cache
entry — which would make an artifact-driven assertion pass on a neighbouring
scenario's cached offers. The per-run suffix extends the same protection across
runs, so `docker compose run --rm verify` against a stack left up by
`KEEP_STACK=1` still fans out for real instead of reading the previous run's
cache.

**Artifact refs are sent as `type: url`, not `type: url_hash`.** That is what a
publisher sends, and the engine derives two different keys from it: the topic
index is keyed on the ref value verbatim, while the signal keyspace is keyed on
`tmproto.HashURL(value)` because raw URLs collide with the signal key's
delimiters. The seeder keys each store the way the path that reads it does, so
a regression in URL canonicalization or hashing breaks the signal scenario.
Sending `url_hash` with a raw URL — which the engine passes through unhashed —
would make both sides agree on a shape neither normalizes and leave that
regression invisible.

**Changes under `targeting/`, `tmproto/`, `urlcanon/` and `urlutil/` do not
reach the images.** Those are separate Go modules, and neither the service
`go.mod` files nor this one replaces them with the working copy — they resolve
from the module proxy at their published version, and `.dockerignore` keeps
`go.work` out of every build context. So the stack builds all four images
against the same *published* store packages, which keeps the seeder and the
agents consistent with each other but means an unreleased change to a key
layout is invisible here. The `e2e-stack` workflow's path filters leave those
directories out for the same reason. `router/` and `registry/` are replaced to
the working copy and are filtered on.

**Some values are necessarily duplicated between `docker-compose.yml` and
`internal/fixture`.** The three services under test are external images, so
their environment cannot import the fixture package. The full list:

| Duplicated | Fixture symbol | What drift does |
| --- | --- | --- |
| the two stub bearer tokens | `RegistryFeedToken`, `ConfigSourceToken` | polls 401, startup fails |
| `PROVIDER_ID` | `ContextProviderID` | suppression scenario returns offers it should not |
| `TMP_OWN_ENDPOINT_URL` ×2 | `ContextAgentEndpoint`, `IdentityAgentEndpoint` | every signature fails |
| `ACCEPTED_TAXONOMIES` | `AcceptedTaxonomy` | the topic gate drops every seeded topic |
| `VALKEY_SHARDS` host ×3 | `ValkeyAddr` | agents cannot reach the store |
| the three `*_VALKEY_DB` numbers | `ContextValkeyDB`, `AudienceValkeyDB`, `FCapValkeyDB` | agents read an empty database |
| `REGISTRY_FEED_URL`, `TMP_REGISTRY_URL` | `RegistryStubBaseURL` | feed sync and key lookup fail |
| `CONFIG_SOURCE_URL` | `ConfigStubBaseURL` | the config poll fails |
| the two stub ports | `RegistryStubPort`, `ConfigStubPort` | health checks fail |

Every one fails loudly rather than silently. Two more sit outside that table and
are not fixture-derived at all: the default subnet is spelled in
`docker-compose.yml`, `run.sh`'s usage block, this file, and the workflow's
job-level `env` (the workflow exports it so its probe and compose agree); and
`run.sh` reads the router's snapshot for the literal string `"signing_keys"`
rather than a key id, precisely so it needs no fixture value.

**Property event payloads are a superset of the spec's shape.** This repo's
`registry.Property` decoder requires `property_id` and `property_type` flat on
the `property.created` payload, while the AdCP schema puts the publisher slug
inside an optional nested `property` object and requires `property_rid` /
`classification` / `identifiers[]` instead. The stub emits both sets. Payload
objects allow additional properties, so the superset is valid on the wire and
consumable by both readers — but a stub that emitted only the spec-required
fields would have every property event dropped as "missing required fields".

## Not covered

- **Dynamic provider discovery** (`discovery.endpoint`). The stack uses a
  static provider list, which is the documented deployment path; discovery
  would add a reconcile loop between the assertions and the provider set.
- **TLS**, on either hop. Both listeners serve cleartext, as they would behind
  an ingress that terminates TLS.
- **TMPX**, verified identity, and the LiveRamp sidecar. All disabled.
- **Multi-shard and cluster Valkey topologies.** Covered by the perf
  harnesses in `bench/`.

## Ports

Published on loopback only — the stub tokens are in this repository, so the
stack must not be reachable from off-box. Override any of them with the
matching environment variable.

| Service | Host port | Variable |
| --- | --- | --- |
| router | 19080 | `ROUTER_PORT` |
| router admin | 19090 | `ROUTER_ADMIN_PORT` |
| context-agent | 19081 | `CONTEXT_AGENT_PORT` |
| context-agent admin | 19091 | `CONTEXT_AGENT_ADMIN_PORT` |
| identity-agent | 19082 | `IDENTITY_AGENT_PORT` |
| identity-agent admin | 19092 | `IDENTITY_AGENT_ADMIN_PORT` |
| registrystub | 19101 | `REGISTRY_PORT` |
| configstub | 19102 | `CONFIG_PORT` |
| valkey | 16379 | `VALKEY_PORT` |

## Relationship to the other harnesses

`bench/context-perf` and `bench/identity-perf` measure one agent under load
against synthetic data at scale. This stack asserts that all three services
are correctly wired to each other at a scale of one request per scenario. The
seeder, the mock-registry and the stub-config-server patterns here started
from those harnesses.

`e2e/` (the parent directory) tests the router in-process against `httptest`
mocks — no containers, no Valkey, no signatures.
