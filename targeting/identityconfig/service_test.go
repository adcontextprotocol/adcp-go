package identityconfig

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSource is a controllable Source backed by an in-memory state, useful
// for exercising Service lifecycle and refresh behavior. Goroutine-safe.
type fakeSource struct {
	mu sync.Mutex

	loadAllErr           error
	loadUpdatedAfterErr  error
	loadAllCalls         atomic.Int64
	loadUpdatedAfterCall atomic.Int64

	configs       map[Key]*targeting.SegmentRule
	lastUpdatedAt time.Time

	deltaQueue []Delta
}

func newFakeSource() *fakeSource {
	return &fakeSource{configs: make(map[Key]*targeting.SegmentRule)}
}

func (f *fakeSource) put(seller, pkg string, rule *targeting.SegmentRule, watermark time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configs[Key{SellerAgentURL: seller, PackageID: pkg}] = rule
	if watermark.After(f.lastUpdatedAt) {
		f.lastUpdatedAt = watermark
	}
}

func (f *fakeSource) queueDelta(d Delta) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deltaQueue = append(f.deltaQueue, d)
}

func (f *fakeSource) setLoadAllError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadAllErr = err
}

func (f *fakeSource) LoadAll(_ context.Context) (Snapshot, error) {
	f.loadAllCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadAllErr != nil {
		return Snapshot{}, f.loadAllErr
	}
	out := Snapshot{LastUpdatedAt: f.lastUpdatedAt}
	for k, rule := range f.configs {
		out.Configs = append(out.Configs, Entry{Key: k, TargetSegments: rule})
	}
	return out, nil
}

func (f *fakeSource) LoadUpdatedAfter(_ context.Context, after time.Time) (Delta, error) {
	f.loadUpdatedAfterCall.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadUpdatedAfterErr != nil {
		return Delta{}, f.loadUpdatedAfterErr
	}
	if len(f.deltaQueue) == 0 {
		return Delta{LastUpdatedAt: after}, nil
	}
	d := f.deltaQueue[0]
	f.deltaQueue = f.deltaQueue[1:]
	return d, nil
}

func TestService_StartLoadsInitialSnapshot(t *testing.T) {
	src := newFakeSource()
	rule := &targeting.SegmentRule{AnyOf: []string{"cooking_fans"}}
	src.put("https://seller.example/agent", "pkg-1", rule, time.Unix(1_000_000_000, 0))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)

	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	got := svc.Get("https://seller.example/agent", "pkg-1")
	require.NotNil(t, got)
	assert.Equal(t, rule, got)

	assert.Equal(t, int64(1), src.loadAllCalls.Load(), "Start should call LoadAll once")
}

func TestService_GetBySellerReturnsAllEntries(t *testing.T) {
	src := newFakeSource()
	r1 := &targeting.SegmentRule{AnyOf: []string{"a"}}
	r2 := &targeting.SegmentRule{AnyOf: []string{"b"}}
	src.put("https://seller.example/agent", "pkg-1", r1, time.Unix(1, 0))
	src.put("https://seller.example/agent", "pkg-2", r2, time.Unix(2, 0))
	src.put("https://other.example/agent", "pkg-3", nil, time.Unix(3, 0))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	entries := svc.GetBySeller("https://seller.example/agent")
	require.Len(t, entries, 2)

	byPkg := make(map[string]*targeting.SegmentRule, len(entries))
	for _, e := range entries {
		byPkg[e.Key.PackageID] = e.TargetSegments
	}
	assert.Equal(t, r1, byPkg["pkg-1"])
	assert.Equal(t, r2, byPkg["pkg-2"])

	assert.Empty(t, svc.GetBySeller("https://unknown.example/agent"))
}

func TestService_FailFastReturnsInitialLoadError(t *testing.T) {
	src := newFakeSource()
	src.setLoadAllError(errors.New("boom"))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	// Service should NOT be in running state after failed FailFast Start.
	// Calling Start again should be permitted.
	src.setLoadAllError(nil)
	require.NoError(t, svc.Start(context.Background()))
	svc.Stop()
}

func TestService_BestEffortSwallowsInitialLoadError(t *testing.T) {
	src := newFakeSource()
	src.setLoadAllError(errors.New("transient"))

	svc, err := New(src, time.Hour, WithStartConfig(StartConfig{Mode: StartModeBestEffort}))
	require.NoError(t, err)

	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	// Empty snapshot still serves lookups (returns nil rule).
	assert.Nil(t, svc.Get("any", "pkg"))
}

func TestService_RetryEventuallySucceeds(t *testing.T) {
	src := newFakeSource()
	src.setLoadAllError(errors.New("not ready yet"))

	// Recover after the second LoadAll call.
	go func() {
		for src.loadAllCalls.Load() < 2 {
			time.Sleep(5 * time.Millisecond)
		}
		src.setLoadAllError(nil)
		src.put("seller", "pkg-1", &targeting.SegmentRule{AnyOf: []string{"x"}}, time.Unix(10, 0))
	}()

	svc, err := New(src, time.Hour, WithStartConfig(StartConfig{
		Mode: StartModeRetry,
		Retry: RetryConfig{
			Initial:     5 * time.Millisecond,
			Max:         20 * time.Millisecond,
			Backoff:     BackoffExponential,
			MaxAttempts: 10,
			Deadline:    2 * time.Second,
		},
	}))
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	require.NotNil(t, svc.Get("seller", "pkg-1"))
}

func TestService_RetryExhaustsAttempts(t *testing.T) {
	src := newFakeSource()
	src.setLoadAllError(errors.New("permanent"))

	svc, err := New(src, time.Hour, WithStartConfig(StartConfig{
		Mode: StartModeRetry,
		Retry: RetryConfig{
			Initial:     time.Millisecond,
			Max:         time.Millisecond,
			Backoff:     BackoffConstant,
			MaxAttempts: 3,
		},
	}))
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 3 attempts")
	assert.GreaterOrEqual(t, src.loadAllCalls.Load(), int64(3))
}

func TestService_DeltaUpsertAndRemove(t *testing.T) {
	src := newFakeSource()
	rule1 := &targeting.SegmentRule{AnyOf: []string{"a"}}
	rule2 := &targeting.SegmentRule{AnyOf: []string{"b"}}
	src.put("seller", "pkg-1", rule1, time.Unix(100, 0))
	src.put("seller", "pkg-2", rule2, time.Unix(100, 0))

	// Queue a delta that removes pkg-1 and replaces pkg-2's rule.
	rule2New := &targeting.SegmentRule{AllOf: []string{"x"}, NoneOf: []string{"y"}}
	src.queueDelta(Delta{
		Upserted:      []Entry{{Key: Key{SellerAgentURL: "seller", PackageID: "pkg-2"}, TargetSegments: rule2New}},
		Removed:       []Key{{SellerAgentURL: "seller", PackageID: "pkg-1"}},
		LastUpdatedAt: time.Unix(200, 0),
	})

	svc, err := New(src, 10*time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	// Wait for the delta refresh tick.
	require.Eventually(t, func() bool {
		return svc.Get("seller", "pkg-1") == nil
	}, time.Second, 5*time.Millisecond, "pkg-1 should be removed")

	got := svc.Get("seller", "pkg-2")
	require.NotNil(t, got)
	assert.Equal(t, rule2New, got)

	assert.Equal(t, time.Unix(200, 0).UTC(), svc.LastUpdatedAt().UTC())
}

func TestService_NoDoubleStart(t *testing.T) {
	svc, err := New(newFakeSource(), time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	err = svc.Start(context.Background())
	assert.Error(t, err)
}

func TestService_StopIsIdempotent(t *testing.T) {
	svc, err := New(newFakeSource(), time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	svc.Stop()
	svc.Stop() // must not panic or block
}

func TestService_New_RejectsBadArgs(t *testing.T) {
	_, err := New(nil, time.Hour)
	assert.Error(t, err)
	_, err = New(newFakeSource(), 0)
	assert.Error(t, err)
}
