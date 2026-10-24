## Repo Context

`adcontextprotocol/adcp-go` is the Go reference implementation of the Ad Context
Protocol (AdCP) and the Trusted Media Protocol (TMP). It ships the router,
targeting engine, `tmproto` signing/verification, `tmpclient`, and reference
agents (context-agent, identity-agent). It is a Go multi-module workspace whose
root module has zero external dependencies. Types are generated from JSON
Schema (`adcp/schemas/*.json` → `adcp/types_gen.go`), and the repo versions via
release-please on conventional commits. Reviews weigh wire-shape fidelity,
schema/generated-type coherence, TMP signing correctness, and tenant/TEE
isolation above style.

### Mandatory: schema ↔ generated-types coherence

`adcp/types_gen.go` is generated (`cd adcp/schemas && python3 generate.py > ../types_gen.go`).
Any change under `adcp/schemas/**` without a corresponding regen of
`adcp/types_gen.go` in the same PR is a `high` finding; any hand-edit to
`adcp/types_gen.go` (the top-of-file banner says it is generated) is a `high`
finding; any `adcp/schemas/VERSION` bump without regen is a `high` finding. A
hand-written struct in `adcp/types.go` is the supported escape hatch only when
its name is listed in `KNOWN_TYPES` in `adcp/schemas/generate.py` (and, for
flattened `oneOf`, in `EXEMPT` in `adcp/schemas/lint.py`); a mismatch that would
fail `python3 adcp/schemas/lint.py --strict` is drift.

### Mandatory: TMP signing / verification semantics

`tmproto/signing.go` and `tmproto/verify_middleware.go` implement the published
TMP request-authentication envelope (Ed25519, `X-AdCP-Signature` /
`X-AdCP-Key-Id`, JCS canonicalization, daily-epoch replay window). Removing or
weakening signature verification, replay-window / daily-epoch checks, or nonce
handling, dropping a live key ID from `RemoteKeyStore` rotation, or changing
canonicalization / signature scheme / header names without a corresponding
`adcontextprotocol/adcp` spec anchor is a `critical`/`high` finding — it breaks
interop or auth silently.

### Mandatory: identity-agent TEE boundary

Changes under `reference/identity-agent/**` that widen the pinhole, skip
attestation, or log plaintext IDs are `critical`/`high` findings.

### Mandatory: protocol-managed skills are read-only here

`skills/adcp-*/**` and `skills/call-adcp-agent/**` are synced from the upstream
`adcontextprotocol/adcp` protocol tarball via `adcp/schemas/download.sh`.
Hand-editing them in this repo is a `high` finding — upstream changes go through
`adcontextprotocol/adcp`. See `skills/README.md`.

### Mandatory: breaking wire change needs a conventional-commit marker

This repo versions via release-please on conventional commits. Removing or
renaming an exported symbol on the wire / public-API path (`adcp/types.go`,
`tmproto/*` envelope and middleware, `router` public `Router*` API, `targeting`
public API, `tmpclient` public API, `cmd/router/main.go` env-var contracts), or
a response-shape / HTTP-status change on a router endpoint, without a `feat!:` /
`fix!:` commit or a `BREAKING CHANGE:` footer on at least one commit is a `high`
finding.

### Mandatory: every minor-release wire change has a pathway back

Per adcontextprotocol/adcp DR-0032 and DR-0033, the SDK lets application code
speak one AdCP release and translates for older counterparts. Minor releases
are non-breaking and backward compatible: on 3.x now, and in each later
major's own minors from 4.1 onward. Every PR that adds or changes support for a
wire field, enum value, task, union variant or semantic rule in a minor release
must also handle the previous minor of the same major, in one of four ways:

- **Droppable:** older counterparts never see it. On a mutating request this
  is allowed only if dropping it does not change spend, targeting, caps or
  account identity.
- **Moves:** it maps to an exact equivalent in the older release, implemented
  in the SDK's version-translation layer for both directions that exist.
- **Refused:** the call fails before sending, with a typed unsupported-feature
  error.
- **Identity-bearing:** it changes an account or resource key, and follows the
  one-to-one reduction rule (never merge two buyer accounts into one seller
  account).

The PR includes a test for its pathway. Missing pathway handling or a missing test is `high`. A
mutation-side field that is dropped when it should be refused is `high`.
Translation that drops or rewrites a field without reporting it is `high`.

## High-Risk Paths

- adcp/schemas/**
- adcp/types.go
- adcp/types_gen.go
- tmproto/**
- router/**
- cmd/router/main.go
- reference/identity-agent/**
- skills/adcp-*/**
- skills/call-adcp-agent/**
- targeting/**

## Escalation Reviewers

- bokelley

## Trivial Paths

- **/*.md
- docs/**
- **/*_test.go
- e2e/**
- go.sum
