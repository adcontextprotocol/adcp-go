// Package identityconfig serves PackageIdentityConfig entries keyed by
// (seller_agent_url, package_id) out of an in-memory snapshot. The Service
// loads the full set from a Source at startup and applies periodic delta
// refreshes — readers see consistent, lock-free views via an atomic snapshot
// swap.
package identityconfig

import (
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
)

// Key uniquely identifies an identity config row. Lookups always combine
// seller_agent_url and package_id; the same package_id under a different
// seller is a different config.
type Key struct {
	SellerAgentURL string
	PackageID      string
}

// Entry pairs a Key with its current TargetSegments rule. A nil rule means
// the package has no audience gating — every user is eligible.
type Entry struct {
	Key            Key
	TargetSegments *targeting.SegmentRule
}

// Snapshot is the result of Source.LoadAll: every known config, plus the
// timestamp of the most recent update across them. LastUpdatedAt is the
// watermark a subsequent LoadUpdatedAfter call uses as its `after` value.
type Snapshot struct {
	Configs       []Entry
	LastUpdatedAt time.Time
}

// Delta is the result of Source.LoadUpdatedAfter: entries added or modified
// since the supplied watermark, entries removed since that watermark, and
// the new watermark. An empty Delta with the same LastUpdatedAt as the caller
// supplied means "no changes."
type Delta struct {
	Upserted      []Entry
	Removed       []Key
	LastUpdatedAt time.Time
}
