package pgreplay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// defaultKeyIDCap mirrors adcp/v3/signing.NewMemoryReplayStore's default of
// 1,000,000 entries per keyid (the spec recommendation). Kept as a local
// constant, not imported, per the module-boundary decision in doc.go — this
// package intentionally has no dependency on adcp/v3/signing.
const defaultKeyIDCap = 1_000_000

// defaultQueryTimeout bounds every round trip PostgresReplayStore makes
// (HitCap, Seen, Insert). ReplayStore's methods don't accept a context — the
// interface predates a distributed implementation — so PostgresReplayStore
// derives a bounded context internally per call. Override with
// WithQueryTimeout.
const defaultQueryTimeout = 3 * time.Second

// defaultPingTimeout bounds the eager connection probe in
// NewPostgresReplayStore.
const defaultPingTimeout = 5 * time.Second

// defaultScope is used when WithScope is not supplied.
const defaultScope = "default"

// ErrConnDown wraps the error returned by InsertContext (and the one
// recorded by LastInsertError / LastSeenError / LastHitCapError) when a
// Postgres round trip itself failed, as opposed to the store correctly
// rejecting the request (nonce already present, or the per-keyid cap
// reached). Check errors.Is(err, ErrConnDown) to tell "the database is
// unreachable" apart from "the request was correctly rejected" — see the
// package doc and the Insert doc comment for why the ReplayStore-interface
// methods still fail closed (reject) in both cases.
var ErrConnDown = errors.New("pgreplay: database round trip failed")

// replayCacheSchema is the DDL PostgresReplayStore expects. IF NOT EXISTS
// makes it safe to run on every deploy from a migration tool that doesn't
// track individual statements — mirrors adcp/idempotency.PostgresSchema's
// convention.
const replayCacheSchema = `
CREATE TABLE IF NOT EXISTS adcp_replay_cache (
    keyid       TEXT NOT NULL,
    scope       TEXT NOT NULL,
    nonce       TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (keyid, scope, nonce)
);

CREATE INDEX IF NOT EXISTS idx_adcp_replay_cache_expires_at
    ON adcp_replay_cache (expires_at);

CREATE INDEX IF NOT EXISTS idx_adcp_replay_cache_keyid_scope_active
    ON adcp_replay_cache (keyid, scope, expires_at);
`

// GetReplayStoreMigration returns the DDL that creates adcp_replay_cache and
// its indexes, per adcontextprotocol/adcp-go#105's schema (also shipped as
// the JS SDK's adcp-client#1018 and Python's adcp-client-python replay
// store). Apply it once via your migration tooling before wiring a
// PostgresReplayStore.
func GetReplayStoreMigration() string { return replayCacheSchema }

// PostgresOption configures a PostgresReplayStore.
type PostgresOption func(*PostgresReplayStore)

// WithScope sets the scope column value this store reads and writes.
// Defaults to "default".
//
// Use it when one Postgres pool/table backs more than one RFC 9421 tag
// profile — for example adcp/request-signing/v1
// (signing.MiddlewareOptions.Replay) and adcp/webhook-signing/v1
// (webhook.VerificationOptions.Replay) both pointed at the same database.
// Construct one *PostgresReplayStore per profile with
// WithScope(profile.Tag) so their nonce namespaces stay isolated — this is
// exactly what the scope column in adcp_replay_cache is for, and matches how
// the JS/Python reference stores use it.
func WithScope(scope string) PostgresOption {
	return func(s *PostgresReplayStore) { s.scope = scope }
}

// WithHitCapLimit sets the per-(keyid, scope) entry cap enforced by HitCap
// and, on a best-effort basis, by Insert (see Insert's doc comment for the
// concurrency caveat). Defaults to 1,000,000, matching
// signing.NewMemoryReplayStore's default. A limit <= 0 passed here is
// treated as "use the default," not "unlimited" — there is no unlimited
// mode, matching MemoryReplayStore's behavior.
func WithHitCapLimit(n int) PostgresOption {
	return func(s *PostgresReplayStore) {
		if n > 0 {
			s.hitCapLimit = n
		}
	}
}

// WithQueryTimeout overrides the per-call timeout applied to HitCap, Seen,
// and Insert's Postgres round trips. Defaults to 3s.
func WithQueryTimeout(d time.Duration) PostgresOption {
	return func(s *PostgresReplayStore) {
		if d > 0 {
			s.queryTimeout = d
		}
	}
}

// WithLogger sets the logger used to record round-trip failures that Insert,
// Seen, and HitCap fail closed on (see the package doc's "Fail-closed
// posture" section). Defaults to slog.Default().
func WithLogger(l *slog.Logger) PostgresOption {
	return func(s *PostgresReplayStore) {
		if l != nil {
			s.logger = l
		}
	}
}

// PostgresReplayStore is a Postgres-backed replay cache implementing the
// same HitCap/Seen/Insert method set as adcp/v3/signing.ReplayStore (see
// package doc for why this package does not import adcp/signing directly).
// All verifier instances sharing one underlying database observe the same
// cache.
//
// Safe for concurrent use — concurrency safety is delegated to Postgres
// itself (the (keyid, scope, nonce) primary key plus
// INSERT ... ON CONFLICT DO NOTHING), not to an in-process mutex.
type PostgresReplayStore struct {
	db           *sql.DB
	scope        string
	hitCapLimit  int
	queryTimeout time.Duration
	logger       *slog.Logger

	lastInsertErr atomic.Pointer[error]
	lastSeenErr   atomic.Pointer[error]
	lastHitCapErr atomic.Pointer[error]
}

// NewPostgresReplayStore returns a PostgresReplayStore bound to db.
//
// It probes db with a PingContext (5s timeout, overridable is not exposed —
// construction is meant to happen once at process wire-up, not on a hot
// path) before returning, and panics with an actionable message if db is nil
// or the probe fails. This deliberately matches
// adcp/idempotency.New's fail-fast convention ("must not start in a state
// where cache writes silently fail") rather than returning an error: the
// suggested API in adcontextprotocol/adcp-go#105 has a single return value,
// and construction failure here is not a condition your verifier should try
// to route around by silently falling back to an in-memory store in
// production — see the package doc's "Test environments" section for the
// gotcha this is closing (adcp#3379) and the recommended gating pattern.
func NewPostgresReplayStore(db *sql.DB, opts ...PostgresOption) *PostgresReplayStore {
	if db == nil {
		panic("pgreplay: NewPostgresReplayStore: db is nil")
	}
	s := &PostgresReplayStore{
		db:           db,
		scope:        defaultScope,
		hitCapLimit:  defaultKeyIDCap,
		queryTimeout: defaultQueryTimeout,
		logger:       slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultPingTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		panic(fmt.Sprintf(
			"pgreplay: NewPostgresReplayStore: database unreachable (check the DSN, network access, and that adcp_replay_cache has been migrated via GetReplayStoreMigration): %v\n\n"+
				"If this is a test or local-dev environment without a real Postgres, do not construct a PostgresReplayStore here — use signing.NewMemoryReplayStore(0) instead. See the pgreplay package doc.",
			err,
		))
	}
	return s
}

// HitCap implements the HitCap/Seen/Insert method set adcp/v3/signing.ReplayStore
// expects: it returns true if the per-(keyid, scope) entry cap has been
// reached.
//
// Fails closed: a Postgres error is treated as "cap hit" (returns true) so
// the caller rejects the request rather than proceeding to an expensive
// crypto verify against a store that may not be able to record the result.
// The underlying error is recorded and retrievable via LastHitCapError, and
// logged at Error level.
func (s *PostgresReplayStore) HitCap(keyid string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.queryTimeout)
	defer cancel()

	var count int
	err := s.db.QueryRowContext(ctx, hitCapSQL, keyid, s.scope).Scan(&count)
	if err != nil {
		wrapped := fmt.Errorf("pgreplay: HitCap: %w: %w", ErrConnDown, err)
		s.lastHitCapErr.Store(&wrapped)
		s.logger.Error("pgreplay: HitCap round trip failed; failing closed", "keyid", keyid, "scope", s.scope, "error", err)
		return true
	}
	s.lastHitCapErr.Store(nil)
	return count >= s.hitCapLimit
}

// Seen implements the HitCap/Seen/Insert method set adcp/v3/signing.ReplayStore
// expects: it returns true if the (keyid, nonce) pair is present (within
// this store's scope) and not yet expired.
//
// Fails closed: a Postgres error is treated as "seen" (returns true) —
// failing open here would mean a database outage silently disables replay
// rejection, which is exactly the failure mode RFC 9421 §11.1 exists to
// prevent. The underlying error is recorded and retrievable via
// LastSeenError, and logged at Error level.
func (s *PostgresReplayStore) Seen(keyid, nonce string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.queryTimeout)
	defer cancel()

	var seen bool
	err := s.db.QueryRowContext(ctx, seenSQL, keyid, s.scope, nonce).Scan(&seen)
	if err != nil {
		wrapped := fmt.Errorf("pgreplay: Seen: %w: %w", ErrConnDown, err)
		s.lastSeenErr.Store(&wrapped)
		s.logger.Error("pgreplay: Seen round trip failed; failing closed", "keyid", keyid, "scope", s.scope, "error", err)
		return true
	}
	s.lastSeenErr.Store(nil)
	return seen
}

// Insert implements the HitCap/Seen/Insert method set adcp/v3/signing.ReplayStore
// expects: it atomically inserts (keyid, scope, nonce) with the given TTL
// and returns true only if this call performed the insert.
//
// # Atomicity
//
// The insert and cap check happen in one round trip: a single statement
// guarded by a WHERE clause on the live-row count for (keyid, scope) and
// ON CONFLICT (keyid, scope, nonce) DO NOTHING. The (keyid, scope, nonce)
// primary key is what makes the nonce-uniqueness half of this genuinely
// atomic under concurrent verifier instances — exactly the race
// adcontextprotocol/adcp-go#105 exists to close: two concurrent Insert calls
// for the same (keyid, scope, nonce), whether from the same process or two
// different verifier instances sharing this database, cannot both return
// true. The cap-check half is best-effort: under a concurrent burst that
// straddles the cap boundary, the WHERE-clause count can be stale between
// two transactions that both evaluate it before either commits, so a small
// amount of cap drift is possible. That mirrors the same caveat a naive
// counter-based Redis implementation would have; ReplayStore's own doc
// comment says distributed stores "SHOULD" (not MUST) make the cap check
// atomic with the insert.
//
// # Return value and failure modes
//
// Three distinct situations all return false, because ReplayStore.Insert's
// signature is bool-only:
//
//  1. The (keyid, scope, nonce) triple already exists (this is the actual
//     replay case, or the losing side of the concurrent-same-nonce race).
//  2. The per-(keyid, scope) cap has been reached.
//  3. The Postgres round trip itself failed (connection down, timeout,
//     context canceled).
//
// Case 3 is deliberately folded into the same "false" / reject outcome as
// cases 1 and 2 — Insert fails closed, the same posture as HitCap and Seen,
// so a database outage rejects signed requests rather than silently
// admitting unverified ones. The verifier surfaces all three as
// request_signature_rate_abuse, which is accurate for cases 1 and 2 and
// misleading for case 3.
//
// adcontextprotocol/adcp-go#54 raised exactly this: collapsing "cap
// rejected" and "couldn't reach the DB" loses operationally important
// information. This package resolves that by exposing InsertContext
// separately — it returns (bool, error) and lets a caller distinguish case 3
// via errors.Is(err, ErrConnDown) — and by recording the same distinction
// via LastInsertError so an operator can build alerting/health checks around
// PostgresReplayStore without depending on the shared ReplayStore interface
// changing. Widening adcp/v3/signing.ReplayStore.Insert itself to
// (bool, error) was deliberately left out of this PR: it is a public
// exported interface with implementers outside this repo (anyone who wrote
// a custom ReplayStore, e.g. an existing Redis-backed one), and the
// compiler here can only verify call sites inside this module — it cannot
// verify or fix external implementations, which a signature change would
// silently break. It's a natural, separately-reviewable follow-up; see the
// PR description for the full reasoning.
func (s *PostgresReplayStore) Insert(keyid, nonce string, ttl time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.queryTimeout)
	defer cancel()

	ok, err := s.InsertContext(ctx, keyid, nonce, ttl)
	if err != nil {
		s.lastInsertErr.Store(&err)
		s.logger.Error("pgreplay: Insert round trip failed; failing closed (rejecting request per RFC 9421 §11.1)", "keyid", keyid, "scope", s.scope, "error", err)
		return false
	}
	s.lastInsertErr.Store(nil)
	return ok
}

// InsertContext is the context-aware, error-distinguishing counterpart to
// Insert. It performs the same atomic insert-with-cap-check described on
// Insert, but returns (false, non-nil error) when the Postgres round trip
// itself failed instead of folding that into a bare false — check
// errors.Is(err, ErrConnDown). A nil error with ok=false means the insert
// was correctly rejected (replay or cap), not that anything failed.
//
// Prefer this method over Insert when wiring a custom ReplayStore adapter or
// building operational tooling (health checks, alerting) around
// PostgresReplayStore; use Insert (or the store as a whole, via the
// ReplayStore interface) when wiring signing.MiddlewareOptions.Replay or
// webhook.VerificationOptions.Replay, which require the bool-only shape.
func (s *PostgresReplayStore) InsertContext(ctx context.Context, keyid, nonce string, ttl time.Duration) (ok bool, err error) {
	if ttl <= 0 {
		return false, fmt.Errorf("pgreplay: InsertContext: ttl must be positive, got %s", ttl)
	}
	expiresAt := time.Now().UTC().Add(ttl)

	var inserted int
	err = s.db.QueryRowContext(ctx, insertSQL, keyid, s.scope, nonce, expiresAt, s.hitCapLimit).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		// WHERE clause guard (cap) or ON CONFLICT DO NOTHING (nonce already
		// present) suppressed the insert. A legitimate rejection, not a
		// round-trip failure.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pgreplay: InsertContext: %w: %w", ErrConnDown, err)
	}
	return inserted == 1, nil
}

// LastInsertError returns the error from the most recent Insert call whose
// Postgres round trip failed (case 3 in Insert's doc comment), or nil if the
// most recent call did not fail that way. Intended for building
// health-check / alerting on top of a wired PostgresReplayStore without
// changing production request handling — the ReplayStore interface itself
// keeps failing closed regardless of what this reports. Safe for concurrent
// use.
func (s *PostgresReplayStore) LastInsertError() error {
	if p := s.lastInsertErr.Load(); p != nil {
		return *p
	}
	return nil
}

// LastSeenError is LastInsertError's counterpart for Seen. Safe for
// concurrent use.
func (s *PostgresReplayStore) LastSeenError() error {
	if p := s.lastSeenErr.Load(); p != nil {
		return *p
	}
	return nil
}

// LastHitCapError is LastInsertError's counterpart for HitCap. Safe for
// concurrent use.
func (s *PostgresReplayStore) LastHitCapError() error {
	if p := s.lastHitCapErr.Load(); p != nil {
		return *p
	}
	return nil
}

// SweepExpiredReplays deletes every adcp_replay_cache row whose expires_at
// has passed and returns the number of rows removed. Postgres has no native
// per-row TTL, so callers are expected to run this periodically (adopter
// cron or pg_cron — scheduling is explicitly out of scope for this package,
// see adcontextprotocol/adcp-go#105's "Out of scope" section).
//
// Safe to run concurrently with HitCap/Seen/Insert and with itself; it only
// ever removes rows already past expiry, so it cannot race a legitimate
// dedup check (an expired row is, by definition, one a WHERE expires_at >
// now() clause has already stopped counting).
func SweepExpiredReplays(ctx context.Context, db *sql.DB) (int, error) {
	res, err := db.ExecContext(ctx, sweepSQL)
	if err != nil {
		return 0, fmt.Errorf("pgreplay: SweepExpiredReplays: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("pgreplay: SweepExpiredReplays: rows affected: %w", err)
	}
	return int(n), nil
}

const hitCapSQL = `
SELECT count(*) FROM adcp_replay_cache
WHERE keyid = $1 AND scope = $2 AND expires_at > now()
`

const seenSQL = `
SELECT EXISTS (
    SELECT 1 FROM adcp_replay_cache
    WHERE keyid = $1 AND scope = $2 AND nonce = $3 AND expires_at > now()
)
`

// insertSQL performs the cap check and the insert in one round trip. The
// WHERE clause on the CTE's count guards the cap (best-effort under
// concurrency, see Insert's doc comment); ON CONFLICT DO NOTHING on the
// (keyid, scope, nonce) primary key is what makes the nonce-uniqueness half
// genuinely atomic. QueryRowContext + Scan(&inserted) distinguishes
// "inserted" (one row, inserted=1) from "suppressed by either guard"
// (sql.ErrNoRows).
const insertSQL = `
WITH capped AS (
    SELECT count(*) AS n FROM adcp_replay_cache
    WHERE keyid = $1 AND scope = $2 AND expires_at > now()
)
INSERT INTO adcp_replay_cache (keyid, scope, nonce, expires_at)
SELECT $1, $2, $3, $4
WHERE (SELECT n FROM capped) < $5
ON CONFLICT (keyid, scope, nonce) DO NOTHING
RETURNING 1
`

const sweepSQL = `DELETE FROM adcp_replay_cache WHERE expires_at <= now()`
