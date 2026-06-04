package suppressionstore

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

// Snapshot serves suppression checks from an in-memory atomic snapshot
// loaded at startup and refreshed periodically. Reads are O(1)
// map-membership and never touch Valkey on the hot path; staleness is
// bounded by the configured refresh interval plus an upstream writer's
// own TTL.
//
// Snapshot satisfies the Reader interface the context engine consumes.
type Snapshot struct {
	store      Store
	providerID string
	snap       atomic.Pointer[snapData]
	logger     *slog.Logger

	// consecutiveFailures and lastRefreshUnix expose the refresh-loop's
	// health to observability code (admin /live, /metrics gauges).
	// consecutiveFailures counts uninterrupted refresh failures since the
	// last success; resets to 0 on every successful Load. lastRefreshUnix
	// is the Unix-second timestamp of the most recent successful Load,
	// independent of whether the snapshot already had data from an
	// earlier refresh.
	consecutiveFailures atomic.Int64
	lastRefreshUnix     atomic.Int64
}

type snapData struct {
	properties map[string]struct{}
	geos       map[string]struct{}
	loadedAt   time.Time
}

// SnapshotConfig configures a Snapshot.
type SnapshotConfig struct {
	Store      Store
	ProviderID string
	Logger     *slog.Logger // nil = slog.Default
}

// NewSnapshot constructs an empty Snapshot. Call Load (or Start) before
// using it for reads; an unloaded snapshot returns false for every
// IsPropertySuppressed / IsGeoSuppressed check, matching the
// fail-open-on-empty-suppression-set behavior at the start of a
// process.
func NewSnapshot(cfg SnapshotConfig) (*Snapshot, error) {
	if cfg.Store == nil {
		return nil, errors.New("suppressionstore: store is required")
	}
	if cfg.ProviderID == "" {
		return nil, errors.New("suppressionstore: provider_id is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Snapshot{store: cfg.Store, providerID: cfg.ProviderID, logger: logger}
	s.snap.Store(&snapData{
		properties: make(map[string]struct{}),
		geos:       make(map[string]struct{}),
	})
	return s, nil
}

// Load pulls every suppression key for the configured provider and
// installs a fresh snapshot. Errors are returned; the existing
// snapshot stays in place on failure. On success, resets the
// consecutive-failure counter and stamps the last-refresh timestamp
// so observability surfaces (ConsecutiveFailures, LastRefresh) see the
// recovery immediately.
func (s *Snapshot) Load(ctx context.Context) error {
	properties, geos, err := LoadAll(ctx, s.store, s.providerID)
	if err != nil {
		return err
	}
	pSet := make(map[string]struct{}, len(properties))
	for _, p := range properties {
		pSet[p] = struct{}{}
	}
	gSet := make(map[string]struct{}, len(geos))
	for _, g := range geos {
		gSet[g] = struct{}{}
	}
	now := time.Now()
	s.snap.Store(&snapData{
		properties: pSet,
		geos:       gSet,
		loadedAt:   now,
	})
	s.lastRefreshUnix.Store(now.Unix())
	s.consecutiveFailures.Store(0)
	return nil
}

// Start runs Load synchronously, then spawns a goroutine that calls
// Load every refresh interval until ctx is cancelled. Refresh failures
// are logged and the previous snapshot is retained.
//
// Returns the error from the initial Load. A caller that wants
// best-effort startup (start with an empty snapshot, populate on the
// first tick) should call NewSnapshot + Start in a goroutine.
func (s *Snapshot) Start(ctx context.Context, refresh time.Duration) error {
	if err := s.Load(ctx); err != nil {
		return err
	}
	if refresh <= 0 {
		return nil
	}
	go s.refreshLoop(ctx, refresh)
	return nil
}

func (s *Snapshot) refreshLoop(ctx context.Context, refresh time.Duration) {
	ticker := time.NewTicker(refresh)
	defer ticker.Stop()
	// During a sustained Valkey outage every tick will fail; logging
	// at WARN on every failure produces 12/h at the 5-minute default
	// and worse at aggressive intervals, drowning real signals.
	// Suppress repeated identical failures and emit one summary
	// every consecutiveFailureLogStride retries instead. The counter
	// itself is mirrored onto s.consecutiveFailures so observability
	// gauges and admin /live can surface the same number.
	const consecutiveFailureLogStride = 12
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			priorFailures := s.consecutiveFailures.Load()
			if err := s.Load(ctx); err != nil {
				failures := s.consecutiveFailures.Add(1)
				if failures == 1 || failures%consecutiveFailureLogStride == 0 {
					s.logger.Warn("suppressionstore: refresh failed; keeping previous snapshot",
						"provider_id", s.providerID,
						"consecutive_failures", failures,
						"error", err)
				}
				continue
			}
			if priorFailures > 0 {
				s.logger.Info("suppressionstore: refresh recovered",
					"provider_id", s.providerID,
					"recovered_after_failures", priorFailures)
			}
		}
	}
}

// IsPropertySuppressed reports whether propertyRID is in the snapshot
// for providerID. The providerID argument matches the engine's
// ContextStorage signature; the snapshot only ever serves the
// providerID it was constructed for. Mismatched IDs return false.
func (s *Snapshot) IsPropertySuppressed(_ context.Context, providerID, propertyRID string) (bool, error) {
	if providerID != s.providerID {
		return false, nil
	}
	d := s.snap.Load()
	if d == nil {
		return false, nil
	}
	_, ok := d.properties[propertyRID]
	return ok, nil
}

// IsGeoSuppressed reports whether country is in the snapshot.
// Mismatched providerID returns false.
func (s *Snapshot) IsGeoSuppressed(_ context.Context, providerID, country string) (bool, error) {
	if providerID != s.providerID {
		return false, nil
	}
	d := s.snap.Load()
	if d == nil {
		return false, nil
	}
	_, ok := d.geos[country]
	return ok, nil
}

// LoadedAt reports when the current snapshot was loaded. Useful for
// staleness metrics.
func (s *Snapshot) LoadedAt() time.Time {
	d := s.snap.Load()
	if d == nil {
		return time.Time{}
	}
	return d.loadedAt
}

// Sizes returns (#suppressed properties, #suppressed geos) in the
// current snapshot. Useful for /metrics or admin endpoints.
func (s *Snapshot) Sizes() (int, int) {
	d := s.snap.Load()
	if d == nil {
		return 0, 0
	}
	return len(d.properties), len(d.geos)
}

// ConsecutiveFailures returns the number of refresh failures since
// the last successful Load. Zero immediately after a successful
// refresh; grows by one per failed refresh attempt. /live can 503
// when this crosses a configurable threshold so the orchestrator
// recycles a pod that has lost its connection to Valkey.
func (s *Snapshot) ConsecutiveFailures() int {
	return int(s.consecutiveFailures.Load())
}

// LastSuccessfulRefresh returns the wall-clock time of the most
// recent successful Load. The zero time means no successful load
// has occurred (Snapshot constructed but Start/Load never called).
func (s *Snapshot) LastSuccessfulRefresh() time.Time {
	unix := s.lastRefreshUnix.Load()
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}
