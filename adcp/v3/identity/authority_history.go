package identity

import (
	"context"
	"sync"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/psl"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/quota"
)

// AuthorityState is what a TrustRecordEvaluator remembers about one
// operator domain.
type AuthorityState struct {
	// Served reports that the operator's trust.json has returned a 200, so
	// a later 404 or 410 is a discovery failure rather than absence: the
	// last record stays authoritative and the brand.json
	// authorized_operators fallback stays disabled (no downgrade).
	Served bool
	// LastRecord is the last record read for the operator, which stays
	// authoritative through an outage. Nil if none has been read.
	LastRecord *EvaluatedTrustRecord
}

// AuthorityHistory persists authority history for a TrustRecordEvaluator.
// It is security state: losing it re-enables the brand.json fallback once a
// brand's trust.json stops resolving, or turns a delisting into an outage. The default keeps it in
// memory, bounded with per-operator quotas, so it does not survive
// restarts or span replicas; public deployments must supply a durable one.
//
// A Load error fails the evaluation closed (the record is reported
// unavailable). A Store error is reported the same way, so an operator the
// store cannot record gets no authority it could later lose silently.
type AuthorityHistory interface {
	LoadAuthority(ctx context.Context, domain string) (AuthorityState, error)
	StoreAuthority(ctx context.Context, domain string, state AuthorityState) error
}

// Default in-memory history bounds. It refuses rather than evicts: at most
// historyPerOperator operators per quota group (psl.QuotaKey, which counts
// every tenant of a hosting platform's PRIVATE suffix together) and
// historyLimit overall. A refused operator simply has no history (a later
// 404 is absence, and an outage with no last record is
// SERVICE_UNAVAILABLE); no other operator's history is evicted.
const (
	historyLimit       = 1 << 16
	historyPerOperator = 64
)

type memoryAuthorityHistory struct {
	mu    sync.Mutex
	table *quota.Table[AuthorityState]
}

func newMemoryAuthorityHistory() *memoryAuthorityHistory {
	return &memoryAuthorityHistory{table: quota.New[AuthorityState](historyLimit, historyPerOperator)}
}

func (m *memoryAuthorityHistory) LoadAuthority(_ context.Context, domain string) (AuthorityState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, _ := m.table.Get(domain)
	return s, nil
}

// StoreAuthority silently refuses a new operator over quota: the default
// degrades to "no history" for that operator rather than failing it.
func (m *memoryAuthorityHistory) StoreAuthority(_ context.Context, domain string, state AuthorityState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.table.Put(domain, historyGroup(domain), state)
	return nil
}

func historyGroup(domain string) string {
	if k, ok := psl.QuotaKey(domain); ok {
		return k
	}
	return domain
}
