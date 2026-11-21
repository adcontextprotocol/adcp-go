# adcp/v3/signing/pgreplay

Postgres-backed `adcp/v3/signing.ReplayStore` for multi-instance RFC 9421 verifier deployments.

Closes [adcontextprotocol/adcp-go#105](https://github.com/adcontextprotocol/adcp-go/issues/105) and [#54](https://github.com/adcontextprotocol/adcp-go/issues/54). Cross-validated against the JS SDK's `PostgresReplayStore` ([adcp-client#1018](https://github.com/adcontextprotocol/adcp-client/pull/1018), in production at agenticadvertising.org) and Python's `adcp-client-python` (`src/adcp/signing/pg/replay_store.py`) — same `(keyid, scope, nonce)` schema, same atomic `INSERT ... ON CONFLICT DO NOTHING` idiom.

**Module note:** both issues' text names the pre-v3 path (`adcp/signing/replay.go`). The root README's "Modules & versioning" section and `MIGRATING.md` say that module is frozen at v2.1.1, security-backports-only — `adcp/v3/signing` is the actively developed one, so this package lives there. `PostgresReplayStore` doesn't import either `signing` package (see below), so it's a structurally valid `ReplayStore` for the frozen `adcp/signing` too, byte-for-byte identical to `adcp/v3/signing.ReplayStore`'s method set as of this writing, if you haven't migrated to v3 yet.

## Why a separate module

`adcp/v3/signing.MemoryReplayStore` dedups `(keyid, nonce)` per process. That's sufficient for one verifier process; it can't deliver RFC 9421 §11.1's replay-rejection MUST once a verifier runs as ≥2 processes behind a load balancer — a captured signature replayed against a sibling instance whose in-memory cache hasn't seen the nonce is accepted. `PostgresReplayStore` gives every instance one shared table instead of N independent caches.

This directory is its own Go module. Production code here imports only `database/sql` — no driver is linked in, so it doesn't strictly *need* isolation to stay dependency-free (`adcp/idempotency`'s own Postgres adapter lives inside the shared `adcp` module today, for exactly that reason: `database/sql` alone doesn't threaten the zero-dep guarantee). The module boundary exists because #54 asked for one explicitly, as a structural, compiler-enforced guarantee that this package can never accidentally grow a real dependency that leaks into core `signing`'s import graph.

## Install

```bash
cd your-project
go get github.com/adcontextprotocol/adcp-go/adcp/v3/signing/pgreplay
```

Bring your own driver (`github.com/jackc/pgx/v5/stdlib`, `github.com/lib/pq`, ...).

## Usage

```go
import (
    "database/sql"

    _ "github.com/jackc/pgx/v5/stdlib"
    "github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
    "github.com/adcontextprotocol/adcp-go/adcp/v3/signing/pgreplay"
)

db, err := sql.Open("pgx", os.Getenv("REPLAY_DATABASE_URL"))
if err != nil {
    log.Fatal(err)
}

// Apply once via your migration tooling:
//   pgreplay.GetReplayStoreMigration()

replay := pgreplay.NewPostgresReplayStore(db) // panics if db is unreachable

mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver: resolver,
    Replay:   replay, // satisfies signing.ReplayStore
    // ...
})
```

Run `pgreplay.SweepExpiredReplays(ctx, db)` on a cron / `pg_cron` job — this package doesn't schedule sweeping itself (out of scope per #105).

## Test and local-dev environments

`NewPostgresReplayStore` probes the connection eagerly and **panics** if it can't reach Postgres. That's deliberate — see the package doc comment for the production incident (adcp#3379) this is closing. **Do not construct a `PostgresReplayStore` in tests or local dev without a real reachable Postgres.** Use `signing.NewMemoryReplayStore(0)` there, gated behind an explicit production/staging environment check:

```go
func replayStore() signing.ReplayStore {
    if os.Getenv("ENVIRONMENT") != "production" && os.Getenv("ENVIRONMENT") != "staging" {
        return signing.NewMemoryReplayStore(0)
    }
    db, err := sql.Open("pgx", os.Getenv("REPLAY_DATABASE_URL"))
    if err != nil {
        log.Fatalf("replay store: %v", err)
    }
    return pgreplay.NewPostgresReplayStore(db)
}
```

## Distinguishing "rejected" from "database is down"

`ReplayStore.Insert` returns a single `bool`, so this package's `Insert` — like `MemoryReplayStore.Insert` — cannot tell a caller "cap rejected" apart from "couldn't reach the DB" through its return value alone ([#54](https://github.com/adcontextprotocol/adcp-go/issues/54) raised this). `PostgresReplayStore` fails closed (returns `false`, i.e. reject) in both cases — a database outage must not silently disable replay rejection. To keep visibility into *which* case happened without changing the shared interface:

- `InsertContext(ctx, keyid, nonce, ttl) (bool, error)` — returns a non-nil error, wrapping `ErrConnDown`, only when the Postgres round trip itself failed.
- `LastInsertError()`, `LastSeenError()`, `LastHitCapError()` — the most recent round-trip error observed by each `ReplayStore`-interface method, for health checks / alerting.

Widening `signing.ReplayStore.Insert` itself to `(bool, error)` was considered and deliberately left out of this PR — see the PR description for the reasoning (it's a public interface with implementers outside this repo; a signature change here can't be verified against them). It's a natural, separately-reviewable follow-up.

## Schema

```sql
CREATE TABLE adcp_replay_cache (
  keyid       TEXT NOT NULL,
  scope       TEXT NOT NULL,
  nonce       TEXT NOT NULL,
  expires_at  TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (keyid, scope, nonce)
);
CREATE INDEX idx_adcp_replay_cache_expires_at ON adcp_replay_cache(expires_at);
CREATE INDEX idx_adcp_replay_cache_keyid_scope_active ON adcp_replay_cache(keyid, scope, expires_at);
```

`scope` lets one Postgres pool back more than one RFC 9421 tag profile (`adcp/request-signing/v1`, `adcp/webhook-signing/v1`) without nonce-namespace collisions — construct one store per profile via `pgreplay.WithScope(profile.Tag)`. Defaults to `"default"`.

## Testing

```bash
go test -race -count=1 ./...                       # unit tests (sqlmock; no Docker needed)
go test -race -tags=integration -count=1 -v ./...   # + real Postgres via testcontainers-go; needs Docker
```

The integration suite proves, against a live `postgres:16-alpine` container:

- **The actual race this package exists to close**: 50 concurrent `Insert` calls for the identical `(keyid, scope, nonce)` yield exactly one winner.
- The `Seen`-then-`Insert` verifier flow under concurrency still yields exactly one winner at the `Insert` step even when multiple callers pass the `Seen` pre-check.
- `HitCap` is enforced against rows `Insert` actually wrote.
- `SweepExpiredReplays` removes only expired rows, leaving live ones untouched.
- `NewPostgresReplayStore` panics against a real (closed) connection, not just a mocked one.
- Two stores on the same database but different `scope`s don't observe each other's nonces.

Integration tests are skipped, not failed, when Docker isn't reachable (`t.Skipf`), matching `registry/redisstore`'s and `registry/glidestore`'s convention. They are not wired into `ci.yml`'s default `go test ./...` step — `-tags=integration` opts in explicitly, same as those two packages today.
