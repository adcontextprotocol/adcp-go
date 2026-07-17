# Argus configuration

## Repo Context

`adcp-go` is the Go reference implementation of the [Ad Context Protocol (AdCP)](https://adcontextprotocol.org) and its Trusted Match Protocol (TMP). The public `adcontextprotocol/adcp` repo owns the spec, JSON schemas, and skill contracts; this repo implements them: router, targeting engine, identity/context reference agents, TMP request-authentication envelope, and an SDK for building AdCP agents. Every wire-level change here must be pinned to a corresponding position in `adcontextprotocol/adcp` — schema regeneration, signing semantics, envelope headers, and pinhole surface are all downstream of that repo.

The rules below layer on top of `AGENTS.md` and `.github/ai-review/expert-adcp-reviewer.md` — they cover ground the base prompt does not.

### Trusted-Match read/write symmetry (fcap `seller_agent_url`)

`fcap.Field.SellerAgentURL` is the marker key on both the reader (`targeting/identityagent/service.go`) and any downstream frequency-writer service. Since PR #406 both sides canonicalize via `github.com/adcontextprotocol/adcp-go/urlcanon` with best-effort semantics: canonical form for well-formed http/https URLs, raw pass-through for non-URL routing strings. A change that reintroduces read/write asymmetry — canonicalizing only one side, or adding a new `fcap.Field{...}` construction site that skips `canonicalizeSellerAgentURL` (defined at `targeting/identityagent/service.go`) — fragments cap buckets by URL spelling and silently fails cap enforcement open. Flag any new `fcap.Field{...}` construction that does not route the seller URL through the helper.

### Router public endpoints and env-var contract

The public router surface is `/registry/snapshot`, `/tmp/context`, and `/tmp/identity` (wired in `cmd/router/main.go`). Env-var contracts use the `TMP_*` prefix — 16 declared vars in `main.go`. Renaming or removing a `TMP_*` var, or changing an HTTP status or response shape on these endpoints, silently breaks operator deploys; combined with the MUST-FIX #6 conventional-commit rule in the base prompt, changes here need a `feat!:` / `fix!:` marker and a matching env-var / chart update called out in the PR description.

### Schema regeneration — version-comment invariant

The base prompt already blocks schema changes without a `types_gen.go` regen. One extra concrete check on top of that: after regen, the version comment at the top of `adcp/types_gen.go` (line 2, `// AdCP schema version: X.Y.Z`) MUST match the contents of `adcp/schemas/VERSION`. A drift there means the regen ran against a stale bundle. Verify with `head -2 adcp/types_gen.go` and `cat adcp/schemas/VERSION`.

### Metrics error-label discipline

`targeting/prommetrics/**` covers the label conventions in general. On top of that: error-outcome labels on identity-agent and context-agent metrics quantize to the closed set `"timeout" | "canceled" | "error"` (see `targeting/identityagent/metrics.go` and `targeting/contextagent/metrics.go`). Never `err.Error()`, never a fresh label per error kind — the set is closed by design and expanding it is a cardinality change that ripples through Grafana dashboards.

### Keep this file in sync

Every rule above is a claim about how the repo actually behaves — file paths, invariants, escalation targets. When a diff changes any of that (a rule becomes stale, a new invariant lands, a boundary moves), the same diff must update `ARGUS.md`. Argus and any other reviewing agent should flag PRs whose behavior changes contradict something in this file without a corresponding `ARGUS.md` edit — an out-of-date `ARGUS.md` is worse than none because it teaches the reviewer the wrong priors.

Applies equally to human contributors and coding agents. Whenever you add, remove, or change something covered here — fcap symmetry, router endpoints / env vars, schema pipeline, metrics discipline, escalation ownership, or when a new class of invariant becomes worth capturing — update this file in the same PR.

Note: `ARGUS.md` is in the self-modification gate (`.github/workflows/ai-review.yml`), so any PR touching this file will pause Argus and require a human reviewer. That is intentional — this file's content is injected verbatim into every review prompt.

## Escalation Reviewers

- bokelley
