package identityconfig

import (
	"context"
	"time"
)

// Source is a backing source of identity configs that the Service polls.
//
// Implementations are responsible for retrieval and parsing only — the
// Service handles snapshot storage, refresh scheduling, and atomic swaps.
// LoadAll and LoadUpdatedAfter may be called concurrently with Get/GetBySeller
// reads, but the Service itself serializes LoadAll/LoadUpdatedAfter calls
// against one another, so a single Source instance does not need to be
// internally reentrant for those two methods.
type Source interface {
	// LoadAll returns every known config along with the watermark the next
	// LoadUpdatedAfter call should use. Called once on Service.Start and
	// MAY be called again by callers performing a manual full reload.
	LoadAll(ctx context.Context) (Snapshot, error)

	// LoadUpdatedAfter returns only the entries added, modified, or removed
	// since `after`. The returned LastUpdatedAt becomes the new watermark.
	// An empty Delta with LastUpdatedAt == after means "no changes."
	LoadUpdatedAfter(ctx context.Context, after time.Time) (Delta, error)
}
