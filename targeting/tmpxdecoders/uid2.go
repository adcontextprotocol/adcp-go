package tmpxdecoders

import (
	"context"
	"errors"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/uid2client"
)

// UID2Client is the subset of *uid2client.Client the UID2 / EUID decoders
// depend on. Declared here as a local interface so unit tests can supply
// a fake without spinning up an httptest server, and so the identityagent
// wire-up can inject a shared client covering both UID2 and EUID.
//
// The interface is intentionally minimal: Decrypt is the only method
// callers need on the hot path. Key refresh runs on the client's own
// background goroutine.
type UID2Client interface {
	Decrypt(ctx context.Context, token string) ([]byte, error)
}

// UID2 decodes a UID2 advertising token string into its raw 32-byte
// identity form. Errors that mean "drop this identity silently" —
// invalid, expired, key-not-found, opted-out — are rewritten to
// [ErrDropFromSeal] so selectEntries removes the identity from the wire
// without failing the whole seal. Any other error surfaces to the
// caller so an operator sees a distinct failure mode (e.g. keys stale,
// scope mismatch — configuration bugs, not per-token drops).
type UID2 struct {
	Client UID2Client
}

// Decode delegates to the configured UID2 client and applies the
// per-token drop-vs-fail mapping.
func (d UID2) Decode(ctx context.Context, userToken string) ([]byte, error) {
	return uid2Lookup(ctx, d.Client, userToken, "uid2")
}

// EUID decodes a European UID (EUID) advertising token. Wire format is
// identical to UID2 modulo the operator URL and identity-scope bit; the
// client the caller supplied must be constructed with
// [uid2client.NewEUIDConfig].
type EUID struct {
	Client UID2Client
}

// Decode delegates to the configured EUID client and applies the
// per-token drop-vs-fail mapping.
func (d EUID) Decode(ctx context.Context, userToken string) ([]byte, error) {
	return uid2Lookup(ctx, d.Client, userToken, "euid")
}

// uid2Lookup is the shared decrypt-and-map helper used by both UID2 and
// EUID decoders. It maps the sentinel errors from [uid2client] onto the
// tmpxdecoders drop signal so the sealer treats a bad or opted-out token
// the same way it treats a LiveRamp miss.
//
// Errors mapped to [ErrDropFromSeal] (silent drop):
//   - uid2client.ErrInvalidToken        (malformed / tampered)
//   - uid2client.ErrTokenExpired        (past expiry)
//   - uid2client.ErrKeyNotFound         (key ID not in cache)
//   - uid2client.ErrVersionUnsupported  (token version we don't parse)
//
// Errors bubbled up (page the operator):
//   - uid2client.ErrKeysStale           (background refresh is broken)
//   - uid2client.ErrScopeMismatch       (wrong client configured)
//   - uid2client.ErrNotInitialized      (New failed silently — shouldn't happen)
//   - anything else                     (unknown; surface it)
func uid2Lookup(ctx context.Context, client UID2Client, token, label string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("%s: client is not configured", label)
	}
	raw, err := client.Decrypt(ctx, token)
	if err != nil {
		switch {
		case errors.Is(err, uid2client.ErrInvalidToken),
			errors.Is(err, uid2client.ErrTokenExpired),
			errors.Is(err, uid2client.ErrKeyNotFound),
			errors.Is(err, uid2client.ErrVersionUnsupported):
			return nil, ErrDropFromSeal
		}
		return nil, fmt.Errorf("%s: decrypt: %w", label, err)
	}
	if len(raw) == 0 {
		return nil, ErrDropFromSeal
	}
	return raw, nil
}
