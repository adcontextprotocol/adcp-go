//go:build integration

// Run with: go test -race -tags=integration -count=1 -v ./...
//
// Requires Docker (spins up a real postgres:16-alpine container via
// testcontainers-go, mirroring registry/redisstore's and
// registry/glidestore's integration_test.go pattern). Skipped, not failed,
// when Docker isn't reachable.
package pgreplay

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startPostgres16 spins up a Postgres 16 container, applies
// GetReplayStoreMigration, and returns a connected *sql.DB. Skipped when
// Docker isn't reachable.
func startPostgres16(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "pgreplay",
			"POSTGRES_PASSWORD": "pgreplay",
			"POSTGRES_DB":       "pgreplay_test",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("Docker not available, skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://pgreplay:pgreplay@%s:%s/pgreplay_test?sslmode=disable", host, port.Port())

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// wait.ForListeningPort proves the TCP port is open, not that Postgres
	// has finished its own startup sequence (it briefly opens/closes the
	// port during initdb). Retry the ping for a bit before giving up.
	deadline := time.Now().Add(30 * time.Second)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres container never became reachable: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}

	_, err = db.ExecContext(ctx, GetReplayStoreMigration())
	require.NoError(t, err, "applying GetReplayStoreMigration")

	return db
}

// TestIntegration_ConcurrentInsertSameNonce_OnlyOneWins is the actual
// correctness proof adcontextprotocol/adcp-go#105 and #54 exist for: N
// concurrent Insert calls for the identical (keyid, scope, nonce) — modeling
// a captured signature replayed against every verifier instance in a pool
// at once — must yield exactly one winner. This is what an in-memory,
// per-process ReplayStore cannot guarantee across processes; it's what the
// (keyid, scope, nonce) primary key + ON CONFLICT DO NOTHING is for.
func TestIntegration_ConcurrentInsertSameNonce_OnlyOneWins(t *testing.T) {
	db := startPostgres16(t)
	store := NewPostgresReplayStore(db)

	const n = 50
	var wg sync.WaitGroup
	var successes atomic.Int64
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // maximize the chance every goroutine races the same instant
			if store.Insert("replayed-keyid", "replayed-nonce", time.Minute) {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(1), successes.Load(),
		"exactly one of %d concurrent Insert calls for the same (keyid, scope, nonce) must win; "+
			"more than one means the replay slipped through, fewer than one means a legitimate first request was rejected", n)

	// The row genuinely exists and Seen() now reports it, proving the winner
	// actually persisted (not e.g. every call silently no-op'ing).
	assert.True(t, store.Seen("replayed-keyid", "replayed-nonce"))
}

// TestIntegration_ConcurrentInsertDistinctNonces_AllSucceed is the negative
// control for the test above: concurrency alone must not cause spurious
// rejections when the nonces actually differ.
func TestIntegration_ConcurrentInsertDistinctNonces_AllSucceed(t *testing.T) {
	db := startPostgres16(t)
	store := NewPostgresReplayStore(db)

	const n = 50
	var wg sync.WaitGroup
	var successes atomic.Int64
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if store.Insert("distinct-keyid", fmt.Sprintf("nonce-%d", i), time.Minute) {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(n), successes.Load())
}

// TestIntegration_SeenThenInsertRace models the actual verifier flow (step
// 12 Seen, step 13 Insert) under concurrency: multiple goroutines each run
// the same Seen-then-Insert sequence a real signing.VerifyRequest call would.
// Seen() can race ahead of a concurrent Insert() and observe "not seen" for
// more than one caller — that's expected and matches
// adcp/signing.ReplayStore's own doc comment (single Seen check is not
// required to be atomic with Insert). What must hold is Insert() itself:
// however many callers reach it for the same nonce, only one may return
// true.
func TestIntegration_SeenThenInsertRace(t *testing.T) {
	db := startPostgres16(t)
	store := NewPostgresReplayStore(db)

	const n = 50
	var wg sync.WaitGroup
	var inserted atomic.Int64
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if store.Seen("verify-flow-keyid", "verify-flow-nonce") {
				return // this goroutine correctly detects the replay pre-insert
			}
			if store.Insert("verify-flow-keyid", "verify-flow-nonce", time.Minute) {
				inserted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(1), inserted.Load(), "at most one goroutine may win the insert regardless of how many passed the Seen pre-check")
}

// TestIntegration_HitCapEnforced proves HitCap observes rows Insert wrote,
// end to end against real Postgres (not sqlmock's SQL-shape approximation).
func TestIntegration_HitCapEnforced(t *testing.T) {
	db := startPostgres16(t)
	store := NewPostgresReplayStore(db, WithHitCapLimit(3), WithScope("hitcap-test"))

	for i := 0; i < 3; i++ {
		ok := store.Insert("capped-keyid", fmt.Sprintf("nonce-%d", i), time.Minute)
		require.True(t, ok, "insert %d should succeed before the cap is reached", i)
	}
	assert.True(t, store.HitCap("capped-keyid"), "cap of 3 should be reached after 3 inserts")

	ok := store.Insert("capped-keyid", "nonce-over-cap", time.Minute)
	assert.False(t, ok, "insert past the cap must be rejected")
}

// TestIntegration_SweepExpiredReplays_RemovesOnlyExpired seeds a mix of
// already-expired and still-live rows directly (bypassing Insert, which
// only ever writes future expiry) and proves the sweep removes exactly the
// expired ones.
func TestIntegration_SweepExpiredReplays_RemovesOnlyExpired(t *testing.T) {
	db := startPostgres16(t)
	ctx := context.Background()

	const scope = "sweep-test"
	seed := func(nonce string, expiresAt time.Time) {
		_, err := db.ExecContext(ctx,
			`INSERT INTO adcp_replay_cache (keyid, scope, nonce, expires_at) VALUES ($1, $2, $3, $4)`,
			"sweep-keyid", scope, nonce, expiresAt)
		require.NoError(t, err)
	}

	seed("expired-1", time.Now().Add(-time.Hour))
	seed("expired-2", time.Now().Add(-time.Minute))
	seed("live-1", time.Now().Add(time.Hour))
	seed("live-2", time.Now().Add(24*time.Hour))

	n, err := SweepExpiredReplays(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "sweep must remove exactly the two expired rows")

	var remaining int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM adcp_replay_cache WHERE scope = $1`, scope).Scan(&remaining))
	assert.Equal(t, 2, remaining, "the two live rows must survive the sweep")

	var remainingExpired int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM adcp_replay_cache WHERE scope = $1 AND expires_at <= now()`, scope).Scan(&remainingExpired))
	assert.Equal(t, 0, remainingExpired, "no expired row should survive the sweep")

	// A second sweep with nothing expired removes nothing.
	n2, err := SweepExpiredReplays(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 0, n2)
}

// TestIntegration_ConstructorPingUnreachable exercises the eager-probe
// gotcha against a real (terminated) container: once the container is gone,
// the pool can no longer dial it, and NewPostgresReplayStore must panic
// rather than hand back a store that will fail closed on every request
// without anyone having noticed at wire-up time.
func TestIntegration_ConstructorPingUnreachable(t *testing.T) {
	db := startPostgres16(t)
	require.NoError(t, db.Ping())

	require.NoError(t, db.Close())

	assert.Panics(t, func() {
		NewPostgresReplayStore(db)
	})
}

// TestIntegration_ScopeIsolation proves two stores sharing one database but
// different scopes (e.g. adcp/request-signing/v1 vs adcp/webhook-signing/v1
// pointed at the same Postgres pool) do not see each other's nonces — the
// scenario WithScope's doc comment describes.
func TestIntegration_ScopeIsolation(t *testing.T) {
	db := startPostgres16(t)
	reqSigning := NewPostgresReplayStore(db, WithScope("adcp/request-signing/v1"))
	webhookSigning := NewPostgresReplayStore(db, WithScope("adcp/webhook-signing/v1"))

	require.True(t, reqSigning.Insert("shared-keyid", "shared-nonce", time.Minute))

	assert.False(t, webhookSigning.Seen("shared-keyid", "shared-nonce"),
		"a different scope must not observe the other scope's nonce")
	assert.True(t, webhookSigning.Insert("shared-keyid", "shared-nonce", time.Minute),
		"the same (keyid, nonce) must be insertable again under a different scope")
}
