package uid2client

import "errors"

// Sentinel errors returned by [Client.Decrypt]. Callers wrap this client
// with a decoder that maps these to a "drop from downstream" sentinel;
// see the errors.Is guidance on each below.
var (
	// ErrNotInitialized is returned by [Client.Decrypt] when the internal
	// key store has never been populated. In practice this cannot happen
	// once [New] returns without error — the constructor requires a
	// successful initial refresh — but callers should still handle it in
	// case the background refresh goroutine has raced them.
	ErrNotInitialized = errors.New("uid2client: keys not initialized")

	// ErrInvalidToken means the input string is not a syntactically valid
	// UID2 or EUID advertising token: wrong length, non-base64 characters,
	// bad prefix byte, bad version byte, malformed encrypted payload, or
	// a GCM/CBC authentication failure. Callers should drop the token.
	ErrInvalidToken = errors.New("uid2client: invalid token")

	// ErrVersionUnsupported means the token's version byte is a value this
	// SDK does not know how to decrypt (i.e. not V2/V3/V4). Bumping the
	// SDK is the fix; on the hot path, callers should treat it the same
	// as ErrInvalidToken (drop).
	ErrVersionUnsupported = errors.New("uid2client: token version not supported")

	// ErrScopeMismatch is returned when a UID2 token is decrypted with an
	// EUID-scoped client, or vice versa. This is a configuration error at
	// the caller — the token is well-formed but belongs to the other
	// scope's client. Not a drop-silent situation; log and fix.
	ErrScopeMismatch = errors.New("uid2client: token identity scope does not match client scope")

	// ErrKeyNotFound is returned when a token references a master or site
	// key ID that isn't in the current key store. This can be transient
	// (a very recent key that hasn't been picked up yet) or permanent (a
	// caller-scope mismatch on operator credentials). Callers should
	// treat it as a drop; a persistent uptick in this counter should
	// trigger investigation into either credentials or refresh cadence.
	ErrKeyNotFound = errors.New("uid2client: referenced key not in store")

	// ErrTokenExpired is returned when the decrypted token's expiry
	// timestamp is at or before "now". Callers should drop the token.
	ErrTokenExpired = errors.New("uid2client: token expired")

	// ErrKeysStale is returned when [Client.Decrypt] observes that every
	// key in the store has expired — i.e. the background refresh
	// goroutine has been failing long enough that the store cannot
	// possibly service a valid token. Distinct from ErrKeyNotFound: the
	// keys were present at some point but their expiry windows have all
	// closed. Callers should page.
	ErrKeysStale = errors.New("uid2client: all cached keys have expired")
)
