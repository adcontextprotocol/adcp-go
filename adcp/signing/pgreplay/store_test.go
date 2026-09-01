package pgreplay

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMock returns a PostgresReplayStore wired to a sqlmock driver, bypassing
// the constructor's eager ping (sqlmock's default ExpectPing behavior treats
// an unset expectation as a no-op success, so NewPostgresReplayStore's probe
// passes without an explicit ExpectPing call — set MonitorPingsOption(true)
// when a test needs to assert on the ping itself).
func newMock(t *testing.T) (*PostgresReplayStore, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(
		sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp),
		sqlmock.MonitorPingsOption(true),
	)
	require.NoError(t, err)
	mock.ExpectPing()
	s := NewPostgresReplayStore(db)
	return s, mock, func() {
		assert.NoError(t, mock.ExpectationsWereMet())
		mock.ExpectClose()
		assert.NoError(t, db.Close())
	}
}

var (
	hitCapRegexp = regexp.MustCompile(`SELECT count\(\*\) FROM adcp_replay_cache`)
	seenRegexp   = regexp.MustCompile(`SELECT EXISTS`)
	insertRegexp = regexp.MustCompile(`INSERT INTO adcp_replay_cache`)
)

// ---- constructor ----

func TestNewPostgresReplayStore_NilDB(t *testing.T) {
	assert.PanicsWithValue(t, "pgreplay: NewPostgresReplayStore: db is nil", func() {
		NewPostgresReplayStore(nil)
	})
}

func TestNewPostgresReplayStore_PingFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectPing().WillReturnError(errors.New("connection refused"))

	assert.Panics(t, func() {
		NewPostgresReplayStore(db)
	}, "constructor must panic (fail loudly) rather than return a store bound to an unreachable database")
}

// TestNewPostgresReplayStore_UnreachableRealDial exercises the eager-probe
// gotcha end to end without a container: dialing a TCP address nothing
// listens on fails fast, so this proves the constructor actually surfaces a
// clear, actionable panic message against a real driver failure mode (not
// just a mocked one). Uses pgx's stdlib adapter, already a module
// dependency for the integration tests.
func TestNewPostgresReplayStore_UnreachableRealDial(t *testing.T) {
	// Port 1 is a reserved/unassigned TCP port; nothing listens there.
	db, err := sql.Open("pgx", "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1&sslmode=disable")
	require.NoError(t, err, "sql.Open must not itself dial — only PingContext should")
	defer func() { _ = db.Close() }()

	defer func() {
		r := recover()
		require.NotNil(t, r, "constructor must panic against an unreachable database")
		msg, ok := r.(string)
		require.True(t, ok)
		assert.Contains(t, msg, "database unreachable")
		assert.Contains(t, msg, "NewMemoryReplayStore", "panic message should point test/dev callers at the in-memory fallback")
	}()
	NewPostgresReplayStore(db)
}

// ---- Insert ----

func TestInsert_Success(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(insertRegexp.String()).
		WithArgs("key1", "default", "nonce1", sqlmock.AnyArg(), defaultKeyIDCap).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))

	ok := s.Insert("key1", "nonce1", time.Minute)
	assert.True(t, ok)
	assert.NoError(t, s.LastInsertError())
}

func TestInsert_AlreadyPresent_ReturnsFalseNoError(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(insertRegexp.String()).
		WithArgs("key1", "default", "nonce1", sqlmock.AnyArg(), defaultKeyIDCap).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"})) // no rows => suppressed by ON CONFLICT or cap guard

	ok := s.Insert("key1", "nonce1", time.Minute)
	assert.False(t, ok)
	assert.NoError(t, s.LastInsertError(), "a legitimate rejection (replay/cap) is not a round-trip error")
}

func TestInsert_DBError_FailsClosedAndRecordsError(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(insertRegexp.String()).
		WithArgs("key1", "default", "nonce1", sqlmock.AnyArg(), defaultKeyIDCap).
		WillReturnError(errors.New("connection reset by peer"))

	ok := s.Insert("key1", "nonce1", time.Minute)
	assert.False(t, ok, "Insert must fail closed on a DB error")

	err := s.LastInsertError()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConnDown), "LastInsertError must be distinguishable via errors.Is(err, ErrConnDown) per adcp-go#54")
}

func TestInsertContext_DistinguishesRejectionFromDBError(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(insertRegexp.String()).
		WithArgs("k", "default", "n", sqlmock.AnyArg(), defaultKeyIDCap).
		WillReturnError(errors.New("timeout"))

	ok, err := s.InsertContext(context.Background(), "k", "n", time.Minute)
	assert.False(t, ok)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConnDown))
}

func TestInsertContext_NonPositiveTTLRejected(t *testing.T) {
	s, _, done := newMock(t)
	defer done()

	ok, err := s.InsertContext(context.Background(), "k", "n", 0)
	assert.False(t, ok)
	assert.Error(t, err)
}

// ---- Seen ----

func TestSeen_True(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(seenRegexp.String()).
		WithArgs("key1", "default", "nonce1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	assert.True(t, s.Seen("key1", "nonce1"))
}

func TestSeen_False(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(seenRegexp.String()).
		WithArgs("key1", "default", "nonce-not-seen").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	assert.False(t, s.Seen("key1", "nonce-not-seen"))
}

func TestSeen_DBError_FailsClosed(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(seenRegexp.String()).
		WithArgs("key1", "default", "nonce1").
		WillReturnError(errors.New("no connection"))

	assert.True(t, s.Seen("key1", "nonce1"), "Seen must fail closed (treat as already-seen) on a DB error")
	err := s.LastSeenError()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConnDown))
}

// ---- HitCap ----

func TestHitCap_False(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(hitCapRegexp.String()).
		WithArgs("key1", "default").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	assert.False(t, s.HitCap("key1"))
}

func TestHitCap_True(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(hitCapRegexp.String()).
		WithArgs("key1", "default").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(defaultKeyIDCap))

	assert.True(t, s.HitCap("key1"))
}

func TestHitCap_DBError_FailsClosed(t *testing.T) {
	s, mock, done := newMock(t)
	defer done()

	mock.ExpectQuery(hitCapRegexp.String()).
		WithArgs("key1", "default").
		WillReturnError(errors.New("db is down"))

	assert.True(t, s.HitCap("key1"), "HitCap must fail closed (treat as capped) on a DB error")
	err := s.LastHitCapError()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConnDown))
}

func TestHitCap_RespectsWithHitCapLimit(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp), sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	s := NewPostgresReplayStore(db, WithHitCapLimit(5))

	mock.ExpectQuery(hitCapRegexp.String()).
		WithArgs("key1", "default").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	assert.True(t, s.HitCap("key1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---- options ----

func TestWithScope_ChangesScopeColumnValue(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp), sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	s := NewPostgresReplayStore(db, WithScope("adcp/webhook-signing/v1"))

	mock.ExpectQuery(seenRegexp.String()).
		WithArgs("key1", "adcp/webhook-signing/v1", "nonce1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	s.Seen("key1", "nonce1")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWithHitCapLimit_NonPositiveIgnored(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	s := NewPostgresReplayStore(db, WithHitCapLimit(-1), WithHitCapLimit(0))
	assert.Equal(t, defaultKeyIDCap, s.hitCapLimit)
}

// ---- migration / sweep SQL shape ----

func TestGetReplayStoreMigration_ContainsExpectedSchema(t *testing.T) {
	ddl := GetReplayStoreMigration()
	assert.Contains(t, ddl, "CREATE TABLE IF NOT EXISTS adcp_replay_cache")
	assert.Contains(t, ddl, "PRIMARY KEY (keyid, scope, nonce)")
	assert.Contains(t, ddl, "idx_adcp_replay_cache_expires_at")
	assert.Contains(t, ddl, "idx_adcp_replay_cache_keyid_scope_active")
}

func TestSweepExpiredReplays_DeletesAndReturnsCount(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectExec(`DELETE FROM adcp_replay_cache WHERE expires_at`).
		WillReturnResult(sqlmock.NewResult(0, 7))

	n, err := SweepExpiredReplays(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, 7, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSweepExpiredReplays_DBError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectExec(`DELETE FROM adcp_replay_cache WHERE expires_at`).
		WillReturnError(errors.New("deadlock"))

	_, err = SweepExpiredReplays(context.Background(), db)
	assert.Error(t, err)
}

// ---- interface shape (compile-time-ish check without importing adcp/signing) ----

// replayStoreShape mirrors adcp/signing.ReplayStore's method set. This
// package deliberately does not import adcp/signing (see doc.go), so this
// local interface is how we assert PostgresReplayStore stays
// structurally assignable to it without adding that dependency.
type replayStoreShape interface {
	HitCap(keyid string) bool
	Seen(keyid, nonce string) bool
	Insert(keyid, nonce string, ttl time.Duration) bool
}

var _ replayStoreShape = (*PostgresReplayStore)(nil)
