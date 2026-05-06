// Package audience provides storage and lookup for audience membership.
//
// State is stored as Valkey hashes keyed by hashed user identity, with one
// field per audience the user belongs to. A parallel set keyed by audience
// ID lists the member hashes so DeleteAudience can cleanly enumerate and
// remove member references.
//
// Schema:
//
//	audience:user:{sha256(user_token)[:16]}     HSET   field=audienceID, value=score (string-encoded float)
//	audience:list:{audienceID}                  SET    members=user-identity hashes
//
// Score 0 indicates membership without an associated score; non-zero scores
// are stored verbatim as decimal strings and parsed back on read.
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
