package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresSchema is the table definition PgBackend expects. Create it once in
// your migration tooling before enabling the backend.
//
// The SDK runs no expiry cleanup. Any cleanup job you add MUST exclude rows
// whose hash starts with '__adcp_in_flight__:' — they are unresolved claims
// whose outcome is unknown and need reconciliation, and deleting one lets
// the key re-execute. For example:
//
//	DELETE FROM adcp_idempotency
//	WHERE expires_at < now() - interval '1 minute'
//	  AND NOT starts_with(hash, '__adcp_in_flight__:');
const PostgresSchema = `
CREATE TABLE IF NOT EXISTS adcp_idempotency (
    scope       TEXT        NOT NULL,
    key         TEXT        NOT NULL,
    hash        TEXT        NOT NULL,
    response    BYTEA       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, key)
);
CREATE INDEX IF NOT EXISTS adcp_idempotency_expires_at_idx
    ON adcp_idempotency (expires_at);
`

// PgBackend is a Postgres-backed Backend. The PRIMARY KEY on (scope, key)
// provides the atomicity PutIfAbsent relies on. Uses database/sql so callers
// can wire any Postgres driver (pgx stdlib adapter, lib/pq, etc.). It never
// deletes expired rows; see PostgresSchema for the cleanup rule on claims.
type PgBackend struct {
	db *sql.DB
}

// NewPgBackend returns a PgBackend bound to db.
func NewPgBackend(db *sql.DB) *PgBackend {
	return &PgBackend{db: db}
}

// Get implements Backend.
func (b *PgBackend) Get(ctx context.Context, scope, key string) (*Entry, error) {
	const q = `SELECT hash, response, created_at, expires_at
	           FROM adcp_idempotency
	           WHERE scope = $1 AND key = $2`
	var e Entry
	err := b.db.QueryRowContext(ctx, q, scope, key).Scan(&e.Hash, &e.Response, &e.CreatedAt, &e.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("idempotency: pg get: %w", err)
	}
	return &e, nil
}

// PutIfAbsent implements Backend via ON CONFLICT DO NOTHING RETURNING: a
// RETURNING row indicates we inserted; no row means an existing entry won
// the race and we re-read it.
func (b *PgBackend) PutIfAbsent(ctx context.Context, scope, key string, entry *Entry) (*Entry, bool, error) {
	const insert = `INSERT INTO adcp_idempotency
	                  (scope, key, hash, response, created_at, expires_at)
	                VALUES ($1, $2, $3, $4, $5, $6)
	                ON CONFLICT (scope, key) DO NOTHING
	                RETURNING hash`
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var gotHash string
	err := b.db.QueryRowContext(ctx, insert, scope, key, entry.Hash, responseBytes(entry.Response), createdAt, entry.ExpiresAt).Scan(&gotHash)
	if err == nil {
		return nil, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("idempotency: pg insert: %w", err)
	}
	existing, err := b.Get(ctx, scope, key)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		// Existing row was deleted between the conflict and our re-read. Treat
		// the slot as open; caller can retry.
		return nil, false, nil
	}
	return existing, false, nil
}

var _ ClaimBackend = (*PgBackend)(nil)

// ReplaceIfHash implements ClaimBackend with a hash-fenced UPDATE.
func (b *PgBackend) ReplaceIfHash(ctx context.Context, scope, key, oldHash string, entry *Entry) (bool, error) {
	const q = `UPDATE adcp_idempotency
	           SET hash = $4, response = $5, created_at = $6, expires_at = $7
	           WHERE scope = $1 AND key = $2 AND hash = $3`
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	res, err := b.db.ExecContext(ctx, q, scope, key, oldHash, entry.Hash, responseBytes(entry.Response), createdAt, entry.ExpiresAt)
	if err != nil {
		return false, fmt.Errorf("idempotency: pg replace: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("idempotency: pg replace: %w", err)
	}
	return n == 1, nil
}

// DeleteIfHash implements ClaimBackend with a hash-fenced DELETE.
func (b *PgBackend) DeleteIfHash(ctx context.Context, scope, key, hash string) (bool, error) {
	const q = `DELETE FROM adcp_idempotency WHERE scope = $1 AND key = $2 AND hash = $3`
	res, err := b.db.ExecContext(ctx, q, scope, key, hash)
	if err != nil {
		return false, fmt.Errorf("idempotency: pg delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("idempotency: pg delete: %w", err)
	}
	return n == 1, nil
}

// responseBytes maps a nil response to an empty slice: the response
// column is NOT NULL and database/sql binds nil []byte as NULL.
func responseBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
