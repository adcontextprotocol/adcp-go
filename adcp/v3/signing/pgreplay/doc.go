// Package pgreplay implements a Postgres-backed adcp/v3/signing.ReplayStore
// for multi-instance RFC 9421 verifier deployments.
//
// # Which module this targets
//
// This package lives under adcp/v3/signing, the actively developed module
// for AdCP 3.x (see the repo root README's "Modules & versioning" section
// and MIGRATING.md): the legacy adcp/signing module is frozen at v2.1.1 and
// receives security backports only, and a new distributed-store feature is
// not a security backport. Both #105 and #54's issue text name the pre-v3
// path (adcp/signing/replay.go) — written before the v3 split, the same way
// #53 did (see adcp/v3/signing/signingtest's own doc comment for that
// precedent). PostgresReplayStore doesn't import either signing package
// (see "Module boundary" below), so it is structurally a drop-in
// implementation of adcp/signing.ReplayStore too, byte-for-byte identical to
// adcp/v3/signing.ReplayStore's method set as of this writing — nothing
// here prevents wiring it into the frozen module if you're not yet on v3.
//
// # Why this exists
//
// adcp/v3/signing.MemoryReplayStore dedups (keyid, nonce) pairs in a
// per-process map. That is sufficient for a single verifier process, but RFC
// 9421 §11.1 makes replay rejection a MUST, and a per-process cache cannot
// deliver it once a verifier runs as more than one process behind a load
// balancer: a captured signature replayed against a sibling instance whose
// cache hasn't seen the nonce is accepted. PostgresReplayStore closes that
// gap by giving every verifier instance one shared table
// (adcp_replay_cache) instead of N independent in-memory caches.
//
// See https://github.com/adcontextprotocol/adcp-go/issues/105 and
// https://github.com/adcontextprotocol/adcp-go/issues/54, and the reference
// implementations this package is cross-validated against: the JS SDK's
// PostgresReplayStore (adcp-client#1018, adopted in production at
// agenticadvertising.org) and Python's adcp-client-python
// (src/adcp/signing/pg/replay_store.py). All three share the
// (keyid, scope, nonce) primary key and the same atomic
// INSERT ... ON CONFLICT DO NOTHING idiom for the nonce race.
//
// # Module boundary
//
// This package is its own Go module (this directory has its own go.mod)
// rather than living inside adcp/v3/signing or the shared adcp/v3 module.
// Production code here imports only database/sql from the standard library
// — no Postgres driver is linked in; callers supply an already-open *sql.DB
// wired to whichever driver they prefer (pgx stdlib adapter, lib/pq, ...).
// That means a separate module isn't required to keep production code
// dependency-free — database/sql alone would do that even living inside
// adcp/v3/signing, the way adcp/idempotency's PgBackend does today (it lives
// in the shared adcp module with zero external deps in its non-test files).
// This package uses a separate module anyway because
// https://github.com/adcontextprotocol/adcp-go/issues/54 asked for one
// explicitly, as a structural, compiler-enforced guarantee that pgreplay can
// never accidentally grow a real dependency (a pgx-specific error type
// check, say) that leaks into core signing's import graph. Test-only
// dependencies (testcontainers-go, go-sqlmock, a driver for integration
// tests) are scoped to this module's go.mod and never reach a consumer that
// only imports the package, exactly as with adcp/idempotency's test-only
// go-sqlmock dependency today.
//
// # Test environments — read this before wiring PostgresReplayStore anywhere
//
// NewPostgresReplayStore probes the connection eagerly and panics with an
// actionable message if it cannot reach Postgres. That is deliberate: the
// JS rollout (adcp#3379) learned that when a PostgresReplayStore is
// constructed against a pool that doesn't exist in the current environment,
// every signed request fails closed identically to "the verifier is
// broken" — a debugging trap if it isn't caught at wire-up. Failing fast in
// the constructor turns that into a startup-time error instead of a
// runtime mystery.
//
// The consequence: do not construct a PostgresReplayStore in tests, local
// dev, or any environment without a real reachable Postgres. Use
// signing.NewMemoryReplayStore(0) there. A typical wiring pattern:
//
//	func replayStore() signing.ReplayStore {
//	    if os.Getenv("ENVIRONMENT") != "production" && os.Getenv("ENVIRONMENT") != "staging" {
//	        return signing.NewMemoryReplayStore(0)
//	    }
//	    db, err := sql.Open("pgx", os.Getenv("REPLAY_DATABASE_URL"))
//	    if err != nil {
//	        log.Fatalf("replay store: %v", err)
//	    }
//	    return pgreplay.NewPostgresReplayStore(db)
//	}
//
// Gate the Postgres path on an explicit production/staging check, not on
// "did REPLAY_DATABASE_URL happen to get set" — a misconfigured production
// deployment should fail loudly, not silently fall back to a per-process
// cache that reintroduces the multi-instance replay gap. This mirrors the
// gated fallback the JS adopter shipped (getReplayStore(), gated on
// NODE_ENV !== 'production').
//
// # Fail-closed posture
//
// HitCap, Seen, and Insert all fail closed: any Postgres error (connection
// down, query timeout, context canceled) is treated as "reject this
// request," never as "allow it through." This matches RFC 9421 §11.1's MUST
// and this SDK's broader safety-over-availability posture — an outage in
// the replay store degrades to rejecting signed requests, not to accepting
// unverified replays. See the doc comments on Insert and LastInsertError for
// how a caller can distinguish a legitimate rejection from a database
// outage without changing the fail-closed behavior itself.
package pgreplay
