package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPgMock(t *testing.T) (*PgBackend, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	b := NewPgBackend(db)
	return b, mock, func() {
		assert.NoError(t, mock.ExpectationsWereMet())
		db.Close()
	}
}

// getRegexp matches the SELECT in PgBackend.Get.
var getRegexp = regexp.MustCompile(`SELECT hash, response, created_at, expires_at\s+FROM adcp_idempotency\s+WHERE scope = \$1 AND key = \$2`)

// putRegexp matches the INSERT in PgBackend.PutIfAbsent.
var putRegexp = regexp.MustCompile(`INSERT INTO adcp_idempotency.*ON CONFLICT .* DO NOTHING\s+RETURNING hash`)

func TestPgGetHit(t *testing.T) {
	b, mock, done := newPgMock(t)
	defer done()

	now := time.Now().UTC()
	mock.ExpectQuery(getRegexp.String()).
		WithArgs("principal:p1", "key-abc").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "response", "created_at", "expires_at"}).
			AddRow("abc123", []byte(`{"x":1}`), now, now.Add(time.Hour)))

	e, err := b.Get(context.Background(), "principal:p1", "key-abc")
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Equal(t, "abc123", e.Hash)
	assert.Equal(t, []byte(`{"x":1}`), e.Response)
}

func TestPgGetMiss(t *testing.T) {
	b, mock, done := newPgMock(t)
	defer done()

	mock.ExpectQuery(getRegexp.String()).
		WithArgs("s", "k").
		WillReturnError(sql.ErrNoRows)

	e, err := b.Get(context.Background(), "s", "k")
	require.NoError(t, err)
	assert.Nil(t, e)
}

func TestPgGetDriverError(t *testing.T) {
	b, mock, done := newPgMock(t)
	defer done()

	mock.ExpectQuery(getRegexp.String()).
		WithArgs("s", "k").
		WillReturnError(errors.New("connection refused"))

	_, err := b.Get(context.Background(), "s", "k")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pg get")
}

func TestPgPutIfAbsentFreshInsert(t *testing.T) {
	b, mock, done := newPgMock(t)
	defer done()

	now := time.Now().UTC()
	entry := &Entry{
		Hash:      "h1",
		Response:  []byte(`{"ok":true}`),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	mock.ExpectQuery(putRegexp.String()).
		WithArgs("s", "k", "h1", entry.Response, now, entry.ExpiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("h1"))

	existing, stored, err := b.PutIfAbsent(context.Background(), "s", "k", entry)
	require.NoError(t, err)
	assert.True(t, stored)
	assert.Nil(t, existing)
}

func TestPgPutIfAbsentConflictReReadReturnsExisting(t *testing.T) {
	b, mock, done := newPgMock(t)
	defer done()

	now := time.Now().UTC()
	entry := &Entry{
		Hash:      "our-hash",
		Response:  []byte(`{"ours":true}`),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	// INSERT … RETURNING yields no row (conflict).
	mock.ExpectQuery(putRegexp.String()).
		WithArgs("s", "k", "our-hash", entry.Response, now, entry.ExpiresAt).
		WillReturnError(sql.ErrNoRows)
	// Re-read returns the row that won the race.
	mock.ExpectQuery(getRegexp.String()).
		WithArgs("s", "k").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "response", "created_at", "expires_at"}).
			AddRow("their-hash", []byte(`{"theirs":true}`), now, now.Add(time.Hour)))

	existing, stored, err := b.PutIfAbsent(context.Background(), "s", "k", entry)
	require.NoError(t, err)
	assert.False(t, stored)
	require.NotNil(t, existing)
	assert.Equal(t, "their-hash", existing.Hash)
}

func TestPgPutIfAbsentConflictReReadEmpty(t *testing.T) {
	// Conflict indicates a row existed, but the re-read finds nothing —
	// either a sweeper deleted it or the conflicting transaction rolled
	// back after our INSERT saw the conflict. PgBackend returns
	// (nil, false, nil) so the middleware can fall back to a fresh Get.
	b, mock, done := newPgMock(t)
	defer done()

	now := time.Now().UTC()
	entry := &Entry{
		Hash:      "h",
		Response:  []byte(`{}`),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	mock.ExpectQuery(putRegexp.String()).
		WithArgs("s", "k", "h", entry.Response, now, entry.ExpiresAt).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(getRegexp.String()).
		WithArgs("s", "k").
		WillReturnError(sql.ErrNoRows)

	existing, stored, err := b.PutIfAbsent(context.Background(), "s", "k", entry)
	require.NoError(t, err)
	assert.False(t, stored)
	assert.Nil(t, existing)
}

func TestPgPutIfAbsentDriverError(t *testing.T) {
	b, mock, done := newPgMock(t)
	defer done()

	now := time.Now().UTC()
	entry := &Entry{Hash: "h", Response: []byte(`{}`), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	mock.ExpectQuery(putRegexp.String()).
		WithArgs("s", "k", "h", entry.Response, now, entry.ExpiresAt).
		WillReturnError(errors.New("deadlock detected"))

	_, _, err := b.PutIfAbsent(context.Background(), "s", "k", entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pg insert")
}

func TestPgPutIfAbsentSetsCreatedAtWhenZero(t *testing.T) {
	b, mock, done := newPgMock(t)
	defer done()

	entry := &Entry{
		Hash:      "h",
		Response:  []byte(`{}`),
		ExpiresAt: time.Now().Add(time.Hour),
		// CreatedAt deliberately zero — PgBackend must fill it.
	}
	mock.ExpectQuery(putRegexp.String()).
		WithArgs("s", "k", "h", entry.Response, sqlmock.AnyArg(), entry.ExpiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("h"))

	_, stored, err := b.PutIfAbsent(context.Background(), "s", "k", entry)
	require.NoError(t, err)
	assert.True(t, stored)
}
