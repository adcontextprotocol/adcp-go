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
// can wire any Postgres driver (pgx stdlib adapter, lib/pq, etc.).
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
	err := b.db.QueryRowContext(ctx, insert, scope, key, entry.Hash, entry.Response, createdAt, entry.ExpiresAt).Scan(&gotHash)
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
