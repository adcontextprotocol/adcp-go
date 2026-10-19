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
| `router/router.go` | Fan-out, merge, circuit breaker. Embeddable via `RouterOption`. TMP request signing wired through `WithTMPSigner` (see `router/signing.go` and the spec's [Request Authentication](https://adcontextprotocol.org/docs/trusted-match/specification#request-authentication) section). |
| `router/signing.go` | Router-side TMP signing — per-provider header attachment, context-match signature cache. |
| `tmproto/signing.go` | TMP request-authentication envelope (Ed25519, `X-AdCP-Signature`/`X-AdCP-Key-Id`, JCS for identity match, daily-epoch replay window). |
| `tmproto/verify_middleware.go` | `VerifyContextMatchHandler` / `VerifyIdentityMatchHandler` middleware used by reference providers. |
| `tmproto/keystore_remote.go` | `RemoteKeyStore` polls the router's `/registry/snapshot` for signing keys. |
| `tmproto/keystore_authorization.go` | `LazyAuthorizationKeyStore` fetches signing keys on demand per `seller_agent_url` from the AdCP registry `/api/registry/authorizations` endpoint. Implements `AgentAwareKeyStore`. |
| `router/serverconfig.go` | Config loading (JSON file, env vars, defaults). |
| `cmd/router/main.go` | Router binary entry point — wires components, Prometheus metrics, env vars. |
| `docs/network-surface.md` | Port map, data flow, pinhole spec, env var reference. |
| `docs/embedding.md` | Guide for embedding the router in host applications. |
| `adcp/serve.go` | One-liner HTTP server for AdCP MCP agents. |
| `adcp/responses.go` | Response builders — `CapabilitiesResponse`, `ProductsResponse`, etc. |
| `adcp/errors.go` | Structured AdCP error builder. |
| `adcp/types.go` | Hand-written AdCP types — oneOf flatteners, inline response items, nested shapes. Generated types live in `adcp/types_gen.go`. |
| `adcp/types_gen.go` | Auto-generated from JSON schemas in `adcp/schemas/` — do not edit. Regenerate via `cd adcp/schemas && python3 generate.py > ../types_gen.go`. |
| `adcp/schemas/generate.py` | JSON-schema-to-Go-struct generator. `KNOWN_TYPES` set (top of file) lists types it skips so they can be hand-written. |
| `adcp/schemas/lint.py` | Drift linter — diffs every `KNOWN_TYPES` entry against its schema. CI fails on drift. |
| `adcp/testcontroller.go` | `RegisterTestController` — comply_test_controller for storyboard testing. |
| `docs/sdk-typing-policy.md` | Decision rules for schema-faithful Go protocol types, optional scalar pointers, enums, `ext`, and generator ownership. |
| `skills/` | SKILL.md files for coding agents. SDK-local (`build-*`) authored here; protocol-managed (`adcp-*`, `call-adcp-agent`) synced by `adcp/schemas/download.sh` from the published bundle — do not hand-edit. CODEOWNERS-gated. See `skills/README.md`. |

### Adding a new AdCP type

1. If the schema exists upstream and the generator produces an acceptable shape: do nothing. Leave `KNOWN_TYPES` alone, let `types_gen.go` own it.
2. If you need a hand-written struct (methods, oneOf flattening, inline response item): add the struct to `adcp/types.go`, add its name to `KNOWN_TYPES` in `adcp/schemas/generate.py`, and regenerate. If the schema is a oneOf that you're flattening into one struct, also add the name to `EXEMPT` in `adcp/schemas/lint.py` so drift-check skips it. The `KNOWN_TYPES` comment block documents the criteria in more detail.
3. Run `cd adcp/schemas && python3 lint.py` locally — it prints missing/extra fields and remediation guidance. CI runs it with `--strict`.

### Updating the schema bundle

1. Run `cd adcp/schemas && ./download.sh <version>`. Released versions require `cosign` on `PATH` and live Sigstore verification; see the `adcp/schemas/download.sh` header for the trust model.
2. Regenerate Go types with `python3 generate.py > ../types_gen.go`.
3. Ensure the PR diff includes both `adcp/schemas/VERSION` and `adcp/schemas/.bundle-sha256`. CI only enables the pinned-bundle shortcut when both files are unchanged from `main`; otherwise, it requires live Sigstore verification. When the files are unchanged, CI sets `ADCP_TRUST_PINNED_BUNDLE=1` and skips live Sigstore verification only after the downloaded bundle hash matches the committed `.bundle-sha256`.

## PR review (Ladon)

Every non-draft, non-dependabot PR is reviewed by Ladon, an LLM PR reviewer that posts `--approve` / `--comment` / `--request-changes` via the AAO Secretariat GitHub App, so its review counts toward the "1 review required" branch-protection check. The workflow lives at `.github/workflows/ai-review.yml` and consumes the shared review engine remotely (`adcontextprotocol/actions/ladon/review@ladon/review/v1`) — voice, expert triage, and review format are maintained upstream in `adcontextprotocol/actions`, so there is nothing to fork or sync here. This repo supplies only its review policy — repo context, MUST-FIX gates, high-risk paths, escalation reviewers, and trivial paths — in `LADON.md` at the repo root. The workflow uses `pull_request_target` with a trusted base-SHA-only checkout (the PR head is never fetched or executed) and skips auto-review on any PR that modifies `LADON.md` or the workflow itself.

### Maintainer questions: five-day lazy consensus

This is an explicit SDK maintainer delegation for contributor PRs in
`adcontextprotocol/adcp-go`, effective when this guidance is merged. It applies
to otherwise merge-ready PRs whose only remaining blocker is an unanswered
question to `@bokelley` about a non-breaking SDK implementation or documentation
choice within the existing protocol contract. It does not change the AdCP WG
constitution, ratify protocol changes, or activate the Spec Guardian design's
WG-wide lazy-consensus rollout.

**Start the clock on the PR.** The author posts a comment that mentions
`@bokelley` and records:

- Each outstanding question and the concrete proposed answer. "Fine as it
  stands" must identify the behavior or wording that will stand.
- The current head SHA and why the question qualifies for this delegation.
- The notice timestamp and an explicit UTC deadline at least five calendar
  days (120 hours, including weekends and holidays) later.

Existing questions do not inherit time elapsed before this policy or before a
complete notice. For example:

```text
@bokelley — five-day SDK consensus notice
Head: <full commit SHA>
Question: <specific implementation or documentation question>
Proposed answer: <concrete default if no objection>
Eligibility: <why this is non-breaking and outside the escalation list>
Notice: <YYYY-MM-DDTHH:MM:SSZ>
Deadline: <YYYY-MM-DDTHH:MM:SSZ, at least 120 hours after notice>
```

**Silence permits the stated default after the deadline.** If no substantive
objection or hold remains, the author, or a maintainer with merge permission,
may merge under the proposed answer. First open or update a tracked issue
recording the question, adopted answer, rationale, and PR/notice links. Leave
a closing PR comment linking that issue and recording the final head SHA and
the elapsed deadline. An explicit maintainer answer can resolve the question
sooner under the ordinary review process.

**A one-line `hold` stops the clock.** Any human reviewer's substantive
objection or explicit hold suspends this delegation; a hold has no automatic
expiry. Resume only after the person who raised it explicitly releases it,
then publish a revised UTC deadline preserving at least the time that
remained. A material change to the implementation, proposed answer, or
eligibility requires a fresh notice and a full five-day window. In particular,
an existing security hold is never cleared by silence.

**All six constitutional escalation categories still require an explicit
decision from the appropriate human authority.** Follow the
[AdCP WG constitution's escalation list](https://github.com/adcontextprotocol/adcp/blob/main/.agents/wg/constitution.md#escalation-list):

1. Breaking changes to the public API, wire shape, semantics, or defaults.
2. Security posture changes, including authentication, signing, transport
   trust, tenant isolation, and the identity-agent TEE boundary.
3. Governance changes: the WG charter, bylaws, IPR policy, constitution,
   decision records, or this delegation itself.
4. Member disputes, recusal questions, and political matters.
5. Release go/no-go for RC and stable cuts.
6. Unresolved expert disagreement after synthesis.

If eligibility is uncertain, escalate rather than starting the clock. Elapsed
time resolves only the listed maintainer question: required approvals, CI,
branch protection, security gates, and blocking review findings still apply.
Address every review comment and resolve every conversation thread before
merge; a tracked issue cannot defer a blocking defect. Verify the exact final
head before recording the consensus outcome and merging.

## Testing

```bash
# Root module (router, targeting, tmproto, tmpclient)
go test ./...

# Sub-modules
cd cmd/router && go test ./...
cd targeting/prommetrics && go test ./...
cd reference/context-agent && go test ./...
cd e2e && go test ./...
```

## Module structure

The workspace uses Go multi-module layout. The root module has zero external dependencies. Sub-modules add deps only where needed:

- `targeting/prommetrics/` — stdlib only (no Prometheus client library)
- `targeting/valkeystore/` — `go-redis/v9`
- `reference/context-agent/` — `RoaringBitmap/roaring`
- `cmd/router/` — `prommetrics`
