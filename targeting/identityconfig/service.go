package identityconfig

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
)

// Service holds an in-memory snapshot of identity configs and refreshes it
// from a Source on a fixed interval. Reads (Get, GetBySeller) are lock-free:
// each refresh constructs a new immutable snapshot and CAS-installs it via
// atomic.Pointer.
type Service struct {
	source   Source
	interval time.Duration
	start    StartConfig
	logger   *slog.Logger

	snap atomic.Pointer[snapshotData]

	mu      sync.Mutex // serializes Start/Stop and concurrent manual refreshes
	cancel  context.CancelFunc
	running bool
	done    chan struct{}
}

// snapshotData is the immutable view installed by every refresh. Readers
// hold the pointer for the duration of their lookup; writers build a new
// snapshotData and atomically swap.
type snapshotData struct {
	byKey         map[Key]*targeting.SegmentRule
	bySeller      map[string][]Entry
	lastUpdatedAt time.Time
}

// New constructs a Service. The returned service has not yet loaded any
// configs — call Start to begin loading and refreshing.
func New(source Source, refreshInterval time.Duration, opts ...Option) (*Service, error) {
	if source == nil {
		return nil, errors.New("identityconfig: source is required")
	}
	if refreshInterval <= 0 {
		return nil, fmt.Errorf("identityconfig: refresh interval must be positive, got %v", refreshInterval)
	}
	s := &Service{
		source:   source,
		interval: refreshInterval,
		start:    StartConfig{Mode: StartModeFailFast},
		logger:   slog.Default(),
	}
	s.snap.Store(emptySnapshot())
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Option configures a Service at construction time.
type Option func(*Service)

// WithStartConfig sets the initial-load failure policy. See StartConfig.
func WithStartConfig(cfg StartConfig) Option {
	return func(s *Service) {
		s.start = cfg
	}
}

// WithLogger sets a structured logger used for refresh-error reporting.
// Passing nil is a no-op — the default slog.Default() logger is kept.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Service) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// Get returns the TargetSegments rule registered under (sellerAgentURL,
// packageID), or nil if no such config exists. A nil return value
// is also valid for "config exists but has no audience gating" — callers
// that need to distinguish absence from a nil rule should use Lookup.
//
// The returned pointer is shared with the current snapshot. Callers MUST
// NOT mutate the rule or its slices; use SegmentRule.Clone if mutation
// is needed.
func (s *Service) Get(sellerAgentURL, packageID string) *targeting.SegmentRule {
	snap := s.snap.Load()
	return snap.byKey[Key{SellerAgentURL: sellerAgentURL, PackageID: packageID}]
}

// Lookup returns the TargetSegments rule registered under (sellerAgentURL,
// packageID) along with a presence flag. The rule itself may be nil even
// when ok is true — that means the config exists but has no audience
// gating. Callers that only need the rule can use Get.
//
// The returned pointer is shared with the current snapshot. Callers MUST
// NOT mutate the rule or its slices; use SegmentRule.Clone if mutation
// is needed.
func (s *Service) Lookup(sellerAgentURL, packageID string) (*targeting.SegmentRule, bool) {
	snap := s.snap.Load()
	rule, ok := snap.byKey[Key{SellerAgentURL: sellerAgentURL, PackageID: packageID}]
	return rule, ok
}

// GetBySeller returns every config registered under the given seller agent
// URL. Used to evaluate requests whose `package_ids` field is omitted: the
// caller resolves the seller's full active package set from the service's
// snapshot rather than the request body.
//
// The returned slice is an independent deep copy: both the entry slots
// and each entry's *SegmentRule are insulated from concurrent snapshot
// updates and from caller-side mutation. Entry order is
// implementation-defined and may differ between snapshots; callers
// needing a stable order must sort the result.
func (s *Service) GetBySeller(sellerAgentURL string) []Entry {
	snap := s.snap.Load()
	entries := snap.bySeller[sellerAgentURL]
	if len(entries) == 0 {
		return nil
	}
	out := make([]Entry, len(entries))
	for i, e := range entries {
		out[i] = Entry{Key: e.Key, TargetSegments: e.TargetSegments.Clone()}
	}
	return out
}

// LastUpdatedAt returns the watermark of the currently installed snapshot.
// Useful for telemetry and health checks.
func (s *Service) LastUpdatedAt() time.Time {
	return s.snap.Load().lastUpdatedAt
}

// Start begins periodic refresh in a background goroutine. The initial
// LoadAll is performed inline before Start returns; its failure handling is
// governed by StartConfig.Mode (and, for StartModeRetry, by RetryConfig).
//
// Stop may be called at any time, including during a long initial load
// (e.g. while StartModeRetry is backing off). Either the supplied ctx or a
// concurrent Stop will interrupt the initial load and cause Start to return.
// After Start returns successfully the refresh loop runs on the same
// internal cancellation token, so Stop continues to halt it.
//
// Calling Start more than once without an intervening Stop is an error.
func (s *Service) Start(ctx context.Context) error {
	loopCtx, loopCancel := context.WithCancel(context.Background())

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		loopCancel()
		return errors.New("identityconfig: Service already started")
	}
	s.running = true
	s.cancel = loopCancel
	s.mu.Unlock()

	// The initial load runs under a context that is cancelled when EITHER
	// the caller's ctx or loopCtx is cancelled. context.AfterFunc bridges
	// loopCtx cancellation (the Stop signal) into the derived context.
	loadCtx, cancelLoad := context.WithCancel(ctx)
	stopLink := context.AfterFunc(loopCtx, cancelLoad)
	loadErr := s.initialLoad(loadCtx)
	stopLink()
	cancelLoad()

	if loadErr != nil {
		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.mu.Unlock()
		loopCancel()
		return loadErr
	}

	done := make(chan struct{})
	s.mu.Lock()
	// If Stop ran between initialLoad's success and here, do not launch
	// the refresh loop. The loopCtx is already cancelled.
	if !s.running {
		s.mu.Unlock()
		loopCancel()
		return errors.New("identityconfig: Service stopped during initial load")
	}
	s.done = done
	s.mu.Unlock()

	go s.refreshLoop(loopCtx, done)
	return nil
}

// Stop halts the refresh loop and blocks until the in-flight refresh (if
// any) returns. Safe to call multiple times.
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	done := s.done
	s.running = false
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// initialLoad performs the very first LoadAll. Behavior on failure is
// dictated by the configured StartMode.
func (s *Service) initialLoad(ctx context.Context) error {
	switch s.start.Mode {
	case StartModeFailFast:
		return s.loadAllOnce(ctx)
	case StartModeBestEffort:
		if err := s.loadAllOnce(ctx); err != nil {
			s.logger.Error("identityconfig: initial load failed; proceeding with empty snapshot", "error", err)
		}
		return nil
	case StartModeRetry:
		return s.loadAllWithRetry(ctx)
	default:
		return fmt.Errorf("identityconfig: unknown start mode %d", s.start.Mode)
	}
}

func (s *Service) loadAllOnce(ctx context.Context) error {
	snap, err := s.source.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("identityconfig: load all: %w", err)
	}
	s.snap.Store(buildSnapshot(snap))
	return nil
}

// loadAllWithRetry retries until success, attempts/deadline exhausted, or
// the context is cancelled.
func (s *Service) loadAllWithRetry(ctx context.Context) error {
	cfg := s.start.Retry
	if cfg.Initial <= 0 {
		cfg.Initial = time.Second
	}
	// Floor Max at Initial. Catches both unset (Max <= 0) and
	// misconfigured (Max < Initial) cases — Initial is positive here.
	if cfg.Max < cfg.Initial {
		cfg.Max = cfg.Initial
	}
	deadlineCtx := ctx
	var cancelDeadline context.CancelFunc
	if cfg.Deadline > 0 {
		deadlineCtx, cancelDeadline = context.WithTimeout(ctx, cfg.Deadline)
		defer cancelDeadline()
	}

	wait := cfg.Initial
	for attempt := 1; ; attempt++ {
		err := s.loadAllOnce(deadlineCtx)
		if err == nil {
			return nil
		}
		if cfg.MaxAttempts > 0 && attempt >= cfg.MaxAttempts {
			return fmt.Errorf("identityconfig: initial load failed after %d attempts: %w", attempt, err)
		}
		s.logger.Warn("identityconfig: initial load failed; retrying",
			"attempt", attempt, "next_in", wait, "error", err)
		timer := time.NewTimer(wait)
		select {
		case <-deadlineCtx.Done():
			timer.Stop()
			return fmt.Errorf("identityconfig: initial load aborted after %d attempts: %w", attempt, deadlineCtx.Err())
		case <-timer.C:
		}
		wait = nextBackoff(wait, cfg)
	}
}

func nextBackoff(current time.Duration, cfg RetryConfig) time.Duration {
	switch cfg.Backoff {
	case BackoffExponential:
		next := min(current*2, cfg.Max)
		return next
	case BackoffConstant:
		fallthrough
	default:
		return cfg.Initial
	}
}

// refreshLoop drives periodic LoadUpdatedAfter calls at the configured
// interval. Errors are logged once per failure and the previous snapshot is
// retained.
func (s *Service) refreshLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.refreshDelta(ctx); err != nil {
				s.logger.Warn("identityconfig: delta refresh failed; keeping prior snapshot", "error", err)
			}
		}
	}
}

func (s *Service) refreshDelta(ctx context.Context) error {
	current := s.snap.Load()
	delta, err := s.source.LoadUpdatedAfter(ctx, current.lastUpdatedAt)
	if err != nil {
		return err
	}
	if len(delta.Upserted) == 0 && len(delta.Removed) == 0 {
		// Watermark-only delta: skip the index rebuild. If the new
		// watermark is meaningfully later, install a copy with the
		// updated watermark so the next LoadUpdatedAfter advances.
		if delta.LastUpdatedAt.After(current.lastUpdatedAt) {
			updated := *current
			updated.lastUpdatedAt = delta.LastUpdatedAt
			s.snap.Store(&updated)
		}
		return nil
	}
	s.snap.Store(applyDelta(current, delta))
	return nil
}

// emptySnapshot returns a zero-value snapshotData with non-nil maps.
func emptySnapshot() *snapshotData {
	return &snapshotData{
		byKey:    make(map[Key]*targeting.SegmentRule),
		bySeller: make(map[string][]Entry),
	}
}

// buildSnapshot turns a Source.LoadAll Snapshot into the immutable
// snapshotData the Service serves reads from.
func buildSnapshot(s Snapshot) *snapshotData {
	out := &snapshotData{
		byKey:         make(map[Key]*targeting.SegmentRule, len(s.Configs)),
		bySeller:      make(map[string][]Entry),
		lastUpdatedAt: s.LastUpdatedAt,
	}
	for _, e := range s.Configs {
		out.byKey[e.Key] = e.TargetSegments
		out.bySeller[e.Key.SellerAgentURL] = append(out.bySeller[e.Key.SellerAgentURL], e)
	}
	return out
}

// applyDelta produces a new snapshotData from the current one with `delta`
// applied. The previous snapshot is left untouched so concurrent readers
// keep working.
func applyDelta(current *snapshotData, delta Delta) *snapshotData {
	newByKey := make(map[Key]*targeting.SegmentRule, len(current.byKey)+len(delta.Upserted))
	maps.Copy(newByKey, current.byKey)
	for _, e := range delta.Upserted {
		newByKey[e.Key] = e.TargetSegments
	}
	for _, k := range delta.Removed {
		delete(newByKey, k)
	}

	newBySeller := make(map[string][]Entry)
	for k, rule := range newByKey {
		newBySeller[k.SellerAgentURL] = append(newBySeller[k.SellerAgentURL], Entry{Key: k, TargetSegments: rule})
	}

	watermark := delta.LastUpdatedAt
	if watermark.Before(current.lastUpdatedAt) {
		watermark = current.lastUpdatedAt
	}
	return &snapshotData{
		byKey:         newByKey,
		bySeller:      newBySeller,
		lastUpdatedAt: watermark,
	}
}
