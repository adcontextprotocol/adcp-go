package webhook

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/adcp/idempotency"
)

// ErrNilIdempotencyKeyPtr is returned by Marshal when a Payload implementation
// returns a nil pointer from IdempotencyKeyPtr. Hand-written implementations
// of Payload can use errors.Is to detect this case.
var ErrNilIdempotencyKeyPtr = errors.New("webhook: Payload.IdempotencyKeyPtr returned nil")

// Payload is any AdCP webhook payload. The generated types in the adcp
// package implement this interface via IdempotencyKeyPtr methods on each of
// the five webhook payload types (MCPWebhookPayload and siblings).
//
// The method returns a pointer (not a getter) so Marshal can mutate the
// field in place — empty strings are filled with a generated UUIDv4 key,
// and that key is written back onto the caller's struct. This makes retry
// sequencing natural: the second call to Marshal with the same struct sees
// the existing key and does not mint a new one.
type Payload interface {
	// IdempotencyKeyPtr returns a writable pointer to the payload's
	// idempotency_key field. Implementations: `return &p.IdempotencyKey`.
	IdempotencyKeyPtr() *string
}

// Marshal generates or validates the payload's idempotency_key, marshals the
// payload to JSON, and returns the bytes bound to that key.
//
// Marshal MUTATES p.IdempotencyKey when the field is empty — it writes a
// freshly-generated UUIDv4 back onto the caller's struct so subsequent calls
// (retries) reuse the same key. To mint a new key for a logically-new event,
// reset p.IdempotencyKey to "" between calls.
//
// Retry contract: senders MUST cache the returned bytes and resend them
// byte-identical on every retry. RFC 9421 Content-Digest verification is
// byte-exact at the receiver, so a re-marshal — even logically-equivalent —
// invalidates the digest and the signature fails at the next hop.
func Marshal(p Payload) (body []byte, key string, err error) {
	kp := p.IdempotencyKeyPtr()
	if kp == nil {
		return nil, "", ErrNilIdempotencyKeyPtr
	}
	if *kp == "" {
		*kp = idempotency.Generate()
	} else if err := idempotency.Validate(*kp); err != nil {
		return nil, "", err
	}
	body, err = json.Marshal(p)
	if err != nil {
		return nil, "", fmt.Errorf("webhook: marshal payload: %w", err)
	}
	return body, *kp, nil
}
