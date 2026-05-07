// Package audience provides storage and lookup for audience membership.
//
// State is stored as Valkey hashes keyed by hashed user identity, with one
// field per audience the user belongs to.
//
// Schema:
//
//	audience:user:{sha256(user_token)[:16]}     HSET   field=audienceID, value=score (string-encoded float)
//
// Score 0 indicates membership without an associated score; non-zero scores
// are stored verbatim as decimal strings and parsed back on read.
//
// The Service owns no audience-side reverse index. Callers that need to
// enumerate members of an audience (e.g., to scrub stored memberships when
// an audience is retired) MUST track that mapping themselves and issue
// remove-side Upserts to clear the relevant fields.
package audience

// Member represents one user's membership in an audience.
type Member struct {
	UserToken string  // raw user identity (hashed by the Service before storage)
	Score     float64 // 0 means membership-only, no score
}

// AudienceUpsert adds and/or removes members for a single audience.
// Both slices may be empty; an empty Upsert is a no-op.
type AudienceUpsert struct {
	AudienceID string
	Add        []Member
	Remove     []string // user tokens to remove
}

// MembershipLookup pairs a user with an audience for batch existence checks.
type MembershipLookup struct {
	UserToken  string
	AudienceID string
}
