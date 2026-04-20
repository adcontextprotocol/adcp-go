package webhook

import (
	"context"
	"errors"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/idempotency"
)

// Options configures a Store.
type Options struct {
	// Backend stores webhook dedup records. Required. It is safe to share the
	// same Backend with the request-side idempotency.Store — webhook keys are
	// scoped under "webhook:sender:<id>", so collisions with request keys
	// (scoped under "principal:<id>") cannot occur.
	Backend idempotency.Backend

	// TTL is the dedup window. Required. Must lie in
	// [idempotency.MinTTL, idempotency.MaxTTL]. The spec does not mandate a
	// webhook-specific value; pick based on your senders' retry horizon
	// (commonly 24h–48h). Values outside the bounds panic in New.
	TTL time.Duration

	// ClockSkew absorbs clock differences at the TTL boundary. Defaults to
	// idempotency.DefaultClockSkew (60s). Negative values panic in New.
	ClockSkew time.Duration

	// Clock is injectable for tests. Defaults to time.Now.UTC.
	Clock func() time.Time
}

// Store deduplicates inbound webhook events by idempotency_key, scoped to the
// authenticated sender identity. Keys from different senders are independent,
// matching the receiver guidance added in adcontextprotocol/adcp#2417.
type Store struct {
	inner *idempotency.Store
}

// NewStore returns a Store. Panics on misconfiguration.
func NewStore(opts Options) *Store {
	inner := idempotency.New(idempotency.Options{
		Backend:   opts.Backend,
		TTL:       opts.TTL,
		ClockSkew: opts.ClockSkew,
		Scope:     webhookScope,
		Clock:     opts.Clock,
	})
	return &Store{inner: inner}
}

// TTL returns the configured dedup window.
func (s *Store) TTL() time.Duration { return s.inner.TTL() }

// Handler is the receiver's business handler. Return nil to commit the dedup
// record (subsequent retries of this idempotency_key will be skipped);
// return an error to reject the delivery so the sender can retry.
type Handler func(ctx context.Context, body []byte) error

// Result describes the outcome of a Dedup call.
type Result struct {
	// Replayed is true when this delivery matched a previously-stored
	// idempotency_key. The handler was NOT invoked.
	Replayed bool

	// Key is the idempotency_key extracted from the payload, for logging.
	Key string
}

// Dedup wraps h with at-most-once processing keyed by the payload's
// idempotency_key. body MUST be the wire bytes as received — the canonical
// JSON hash used to detect "same key, different payload" relies on the bytes
// the sender signed, not a re-marshaled struct.
//
// ctx MUST carry a sender identity set via WithSender. Unscoped dedup would
// let one sender observe another sender's state.
//
// Errors map to idempotency package types:
//   - *idempotency.MissingKeyError — body has no idempotency_key
//   - *idempotency.InvalidKeyError — idempotency_key fails format validation
//   - *idempotency.ConflictError   — key seen before with a different payload
//   - *idempotency.ExpiredError    — key was valid but is past TTL
//
// Handler errors are propagated unwrapped.
func (s *Store) Dedup(ctx context.Context, body []byte, h Handler) (*Result, error) {
	if h == nil {
		return nil, errors.New("webhook: Handler is required")
	}
	wrapped := s.inner.Wrap(func(ctx context.Context, req []byte) ([]byte, error) {
		return nil, h(ctx, req)
	})
	inner, err := wrapped(ctx, body)
	if err != nil {
		return nil, err
	}
	return &Result{Replayed: inner.Replayed, Key: inner.Key}, nil
}

// webhookScope scopes a webhook key to the authenticated sender identity.
// The "webhook:" prefix ensures the scope namespace is disjoint from the
// request-side "principal:" scopes, so a shared Backend is safe.
func webhookScope(ctx context.Context, _ []byte) (string, error) {
	sender := SenderFromContext(ctx)
	if sender == "" {
		return "", errors.New("webhook: sender identity missing from context; set via WithSender")
	}
	return "webhook:sender:" + sender, nil
}

type senderKey struct{}

// WithSender attaches an authenticated sender identity to ctx. Dedup scope is
// (webhook, sender) — receivers MUST set this from whichever auth mechanism
// they use (RFC 9421 keyid, HMAC credential ID, Bearer principal, etc.).
func WithSender(ctx context.Context, senderID string) context.Context {
	return context.WithValue(ctx, senderKey{}, senderID)
}

// SenderFromContext returns the sender previously set via WithSender, or "".
func SenderFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(senderKey{}).(string); ok {
		return v
	}
	return ""
}
