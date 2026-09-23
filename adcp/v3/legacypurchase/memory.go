package legacypurchase

import (
	"context"
	"sync"
	"time"
)

// MemoryBackend is an in-process Backend suitable for tests and reference
// servers, mirroring adcp/v3/idempotency.MemoryBackend's shape and
// concurrency guarantees. All state transitions are guarded by one mutex,
// so ClaimPending/CompletePending/FailPending are trivially atomic; a
// distributed Backend (e.g. Postgres, deliberately not shipped in this PR —
// see doc.go) would use row-level locking or a conditional UPDATE to get
// the same guarantee across processes.
//
// A background sweeper removes StateOffered records past ExpiresAt and
// terminal (StateCommitted/StateFailed) records past a configurable
// retention window. StatePending records are never swept — an ambiguous,
// crash-suspected claim must be reconciled, not silently vanish.
type MemoryBackend struct {
	mu      sync.Mutex
	records map[string]*ContinuationRecord
	clock   func() time.Time

	terminalRetention time.Duration

	stop    chan struct{}
	stopped chan struct{}
}

// NewMemoryBackend returns a MemoryBackend. sweepInterval controls how
// often expired StateOffered records and terminal records older than
// terminalRetention are removed; zero disables the background sweeper
// (expired StateOffered records still fail closed via ExpiredError at
// redemption time — the sweeper only reclaims memory). terminalRetention
// of zero keeps terminal records forever (until the sweeper is disabled or
// Close is called).
func NewMemoryBackend(sweepInterval, terminalRetention time.Duration) *MemoryBackend {
	return newMemoryBackend(sweepInterval, terminalRetention, time.Now)
}

func newMemoryBackend(sweepInterval, terminalRetention time.Duration, clock func() time.Time) *MemoryBackend {
	b := &MemoryBackend{
		records:           map[string]*ContinuationRecord{},
		clock:             clock,
		terminalRetention: terminalRetention,
		stop:              make(chan struct{}),
		stopped:           make(chan struct{}),
	}
	if sweepInterval > 0 {
		go b.sweepLoop(sweepInterval)
	} else {
		close(b.stopped)
	}
	return b
}

// Close stops the background sweeper.
func (b *MemoryBackend) Close() {
	select {
	case <-b.stop:
		return
	default:
		close(b.stop)
	}
	<-b.stopped
}

func (b *MemoryBackend) sweepLoop(interval time.Duration) {
	defer close(b.stopped)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			b.sweep()
		}
	}
}

func (b *MemoryBackend) sweep() {
	now := b.clock()
	b.mu.Lock()
	defer b.mu.Unlock()
	for token, rec := range b.records {
		switch rec.State {
		case StateOffered:
			if now.After(rec.ExpiresAt) {
				delete(b.records, token)
			}
		case StateCommitted, StateFailed:
			if b.terminalRetention > 0 && now.After(rec.CompletedAt.Add(b.terminalRetention)) {
				delete(b.records, token)
			}
		case StatePending:
			// Never swept — see type doc.
		}
	}
}

// cloneRecord returns a deep copy of rec: besides copying the struct itself,
// it copies the backing arrays of every mutable slice/byte-slice field
// (ProductIDs, Losses, ObservedPayload, Result) so the clone shares no
// memory with rec. Every accessor below returns a clone rather than a
// pointer sharing rec's slices — otherwise a caller mutating a byte in a
// returned record's Result (say) would silently corrupt what this backend
// has stored, or a caller later mutating a slice it handed to
// PutContinuation would silently corrupt state already durably recorded.
func cloneRecord(rec *ContinuationRecord) *ContinuationRecord {
	cp := *rec
	cp.ProductIDs = append([]string(nil), rec.ProductIDs...)
	cp.Losses = append([]string(nil), rec.Losses...)
	cp.ObservedPayload = append([]byte(nil), rec.ObservedPayload...)
	cp.Result = append([]byte(nil), rec.Result...)
	return &cp
}

// PutContinuation implements Backend.
func (b *MemoryBackend) PutContinuation(_ context.Context, rec *ContinuationRecord) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.records[rec.Token]; exists {
		return &DuplicateTokenError{Token: rec.Token}
	}
	b.records[rec.Token] = cloneRecord(rec)
	return nil
}

// GetContinuation implements Backend.
func (b *MemoryBackend) GetContinuation(_ context.Context, token string) (*ContinuationRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.records[token]
	if !ok {
		return nil, nil
	}
	return cloneRecord(rec), nil
}

// ClaimPending implements Backend.
func (b *MemoryBackend) ClaimPending(_ context.Context, token, claimantKey, requestHash string, claimedAt time.Time) (*ContinuationRecord, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.records[token]
	if !ok {
		return nil, false, nil
	}
	if rec.State != StateOffered {
		return cloneRecord(rec), false, nil
	}
	rec.State = StatePending
	rec.ClaimantKey = claimantKey
	rec.RequestHash = requestHash
	rec.ClaimedAt = claimedAt
	return cloneRecord(rec), true, nil
}

// CompletePending implements Backend.
func (b *MemoryBackend) CompletePending(_ context.Context, token, claimantKey string, result []byte, completedAt time.Time) (*ContinuationRecord, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.records[token]
	if !ok || rec.State != StatePending || rec.ClaimantKey != claimantKey {
		if ok {
			return cloneRecord(rec), false, nil
		}
		return nil, false, nil
	}
	rec.State = StateCommitted
	rec.Result = append([]byte(nil), result...)
	rec.CompletedAt = completedAt
	return cloneRecord(rec), true, nil
}

// FailPending implements Backend.
func (b *MemoryBackend) FailPending(_ context.Context, token, claimantKey, errCode, errMessage string, failedAt time.Time) (*ContinuationRecord, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.records[token]
	if !ok || rec.State != StatePending || rec.ClaimantKey != claimantKey {
		if ok {
			return cloneRecord(rec), false, nil
		}
		return nil, false, nil
	}
	rec.State = StateFailed
	rec.ErrorCode = errCode
	rec.ErrorMessage = errMessage
	rec.CompletedAt = failedAt
	return cloneRecord(rec), true, nil
}
