// Package fcap provides frequency-cap state storage for the targeting engine.
//
// State is stored as Valkey hashes keyed by user identity. Each hash field
// represents a capped (seller_agent_url, package_id) tuple and carries a TTL
// equal to the end of the current cap window. A field's existence means
// "this user has reached the cap on this package for this seller until the
// field expires." The store never tracks per-impression counts — callers
// decide when the cap fires (i.e., on the last allowed exposure).
package fcap

// Field identifies a capped (seller_agent_url, package_id) tuple under a
// user identity hash.
type Field struct {
	SellerAgentURL string
	PackageID      string
}
