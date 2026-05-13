package identityconfig

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memorySource is a stateful in-memory implementation of Source for tests:
// callers put configs and queue deltas, and the implementation serves them
// through the Source contract. This is a fake, not a mock — there are no
// per-call expectations. For call-by-call EXPECT-style assertions, use a
// generated mock instead.
//
// Goroutine-safe.
type memorySource struct {
	mu sync.Mutex

	loadAllErr           error
	loadUpdatedAfterErr  error
	loadAllCalls         atomic.Int64
	loadUpdatedAfterCall atomic.Int64

	configs       map[Key]*targeting.SegmentRule
	lastUpdatedAt time.Time

	deltaQueue []Delta
}

func newMemorySource() *memorySource {
	return &memorySource{configs: make(map[Key]*targeting.SegmentRule)}
}

func (m *memorySource) put(seller, pkg string, rule *targeting.SegmentRule, watermark time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[Key{SellerAgentURL: seller, PackageID: pkg}] = rule
	if watermark.After(m.lastUpdatedAt) {
		m.lastUpdatedAt = watermark
	}
}

func (m *memorySource) queueDelta(d Delta) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deltaQueue = append(m.deltaQueue, d)
}

func (m *memorySource) setLoadAllError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadAllErr = err
}

func (m *memorySource) LoadAll(_ context.Context) (Snapshot, error) {
	m.loadAllCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadAllErr != nil {
		return Snapshot{}, m.loadAllErr
	}
	out := Snapshot{LastUpdatedAt: m.lastUpdatedAt}
	for k, rule := range m.configs {
		out.Configs = append(out.Configs, Entry{Key: k, TargetSegments: rule})
	}
	return out, nil
}

func (m *memorySource) LoadUpdatedAfter(_ context.Context, after time.Time) (Delta, error) {
	m.loadUpdatedAfterCall.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadUpdatedAfterErr != nil {
		return Delta{}, m.loadUpdatedAfterErr
	}
	if len(m.deltaQueue) == 0 {
		return Delta{LastUpdatedAt: after}, nil
	}
	d := m.deltaQueue[0]
	m.deltaQueue = m.deltaQueue[1:]
	return d, nil
}

func TestService_StartLoadsInitialSnapshot(t *testing.T) {
	src := newMemorySource()
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
	src := newMemorySource()
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
	src := newMemorySource()
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
	src := newMemorySource()
	src.setLoadAllError(errors.New("transient"))

	svc, err := New(src, time.Hour, WithStartConfig(StartConfig{Mode: StartModeBestEffort}))
	require.NoError(t, err)

	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	// Empty snapshot still serves lookups (returns nil rule).
	assert.Nil(t, svc.Get("any", "pkg"))
}

func TestService_RetryEventuallySucceeds(t *testing.T) {
	src := newMemorySource()
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
	src := newMemorySource()
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
	src := newMemorySource()
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
	svc, err := New(newMemorySource(), time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	err = svc.Start(context.Background())
	assert.Error(t, err)
}

func TestService_StopIsIdempotent(t *testing.T) {
	svc, err := New(newMemorySource(), time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	svc.Stop()
	svc.Stop() // must not panic or block
}

func TestService_New_RejectsBadArgs(t *testing.T) {
	_, err := New(nil, time.Hour)
	assert.Error(t, err)
	_, err = New(newMemorySource(), 0)
	assert.Error(t, err)
}

// blockingSource holds LoadUpdatedAfter inside a channel until released.
// Exercises Stop-vs-in-flight-refresh ordering.
type blockingSource struct {
	loadAllSrc *memorySource
	release    chan struct{}
	entered    chan struct{}
	once       sync.Once
}

func (b *blockingSource) LoadAll(ctx context.Context) (Snapshot, error) {
	return b.loadAllSrc.LoadAll(ctx)
}

func (b *blockingSource) LoadUpdatedAfter(ctx context.Context, _ time.Time) (Delta, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return Delta{}, ctx.Err()
	}
	return Delta{LastUpdatedAt: time.Now()}, nil
}

func TestService_StopUnblocksInFlightRefresh(t *testing.T) {
	bs := &blockingSource{
		loadAllSrc: newMemorySource(),
		release:    make(chan struct{}),
		entered:    make(chan struct{}),
	}
	svc, err := New(bs, 5*time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))

	// Wait for the refresh loop to enter LoadUpdatedAfter.
	select {
	case <-bs.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("LoadUpdatedAfter was never called")
	}

	// Stop must return promptly even though the source is blocked: the
	// ctx cancel propagates into LoadUpdatedAfter via ctx.Done().
	stopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		close(bs.release) // unblock to let the test finish, then fail
		t.Fatal("Service.Stop did not return promptly while a refresh was in flight")
	}
}

func TestService_ConcurrentReadsDuringRefresh(t *testing.T) {
	src := newMemorySource()
	for i := range 50 {
		src.put("seller", fmt.Sprintf("pkg-%d", i), &targeting.SegmentRule{AnyOf: []string{"x"}}, time.Unix(1, 0))
	}

	svc, err := New(src, 2*time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	// Queue a sequence of deltas. The refresh ticker installs them as
	// it goes; concurrent readers must always see a consistent snapshot.
	for i := range 20 {
		seg := fmt.Sprintf("seg-%d", i)
		src.queueDelta(Delta{
			Upserted:      []Entry{{Key: Key{SellerAgentURL: "seller", PackageID: "pkg-0"}, TargetSegments: &targeting.SegmentRule{AnyOf: []string{seg}}}},
			LastUpdatedAt: time.Unix(int64(2+i), 0),
		})
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Reads must never panic, return torn data, or race.
				_ = svc.Get("seller", "pkg-0")
				_ = svc.GetBySeller("seller")
				_ = svc.LastUpdatedAt()
			}
		}()
	}
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestService_GetBySellerCopyIsolated(t *testing.T) {
	src := newMemorySource()
	src.put("seller", "pkg-1", &targeting.SegmentRule{AnyOf: []string{"a"}}, time.Unix(1, 0))
	svc, err := New(src, time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	first := svc.GetBySeller("seller")
	require.Len(t, first, 1)
	// Mutate the returned slice — must not affect the snapshot.
	first[0] = Entry{Key: Key{SellerAgentURL: "seller", PackageID: "tampered"}}

	second := svc.GetBySeller("seller")
	require.Len(t, second, 1)
	assert.Equal(t, "pkg-1", second[0].Key.PackageID, "snapshot must be insulated from caller mutation")
}

func TestService_GetBeforeStartReturnsNil(t *testing.T) {
	svc, err := New(newMemorySource(), time.Hour)
	require.NoError(t, err)
	// No Start, no panic — empty snapshot is installed by the constructor.
	assert.Nil(t, svc.Get("seller", "pkg"))
	assert.Empty(t, svc.GetBySeller("seller"))
	_, present := svc.Lookup("seller", "pkg")
	assert.False(t, present)
}
