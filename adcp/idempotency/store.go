package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Spec-mandated bounds on replay_ttl_seconds: 1 hour minimum, 7 days maximum.
const (
	MinTTL = 1 * time.Hour
	MaxTTL = 7 * 24 * time.Hour
	// DefaultClockSkew is the spec's ±60s tolerance around the TTL boundary.
	DefaultClockSkew = 60 * time.Second
)

// Options configures a Store.
type Options struct {
	// Backend stores idempotency records. Required.
	Backend Backend

	// TTL is the replay window surfaced via Capability() and enforced on
	// lookup. Required. Must be in [MinTTL, MaxTTL].
	TTL time.Duration

	// ClockSkew is the tolerance applied at the TTL boundary to absorb small
	// clock differences between client and server. A request that arrives
	// ClockSkew past TTL is still served from cache. Defaults to
	// DefaultClockSkew (60s). Set to 0 to disable.
	ClockSkew time.Duration

	// KeyRequired controls whether a missing idempotency_key triggers
	// MissingKeyError. Defaults to true. Set false for si_terminate_session
	// and other tools where the spec makes the key optional; when false and
	// no key is present, the handler runs uncached.
	KeyRequired *bool

	// Hash canonicalizes a request payload and returns a stable digest.
	// Defaults to CanonicalJSONSHA256 (JCS canonicalization with
	// DefaultExcludePaths stripped, SHA-256 applied). Provide a custom
	// NewCanonicalJSONSHA256(customPaths) if you need to override the
	// exclude list.
	Hash HashFn

	// Scope extracts the scope under which keys are unique for a given
	// request context. Defaults to PrincipalScope, which reads the principal
	// from context (set via WithPrincipal).
	Scope ScopeFn

	// Clock is injectable for tests. Defaults to time.Now.UTC.
	Clock func() time.Time
}

// ScopeFn derives the storage scope for a request. Per-principal scope is a
// security requirement: keys from different principals MUST NOT collide.
// A handler may return a richer scope (e.g. "principal:sess") to narrow
// uniqueness further — si_send_message scopes to (principal, session_id).
type ScopeFn func(ctx context.Context, payload []byte) (string, error)

// Store holds idempotency configuration and wraps mutating handlers.
type Store struct {
	opts        Options
	keyRequired bool
}

// New returns a Store. Panics on misconfiguration — the middleware must not
// start in a state where cache writes silently fail.
func New(opts Options) *Store {
	if opts.Backend == nil {
		panic("idempotency: Options.Backend is required")
	}
	if opts.TTL < MinTTL || opts.TTL > MaxTTL {
		panic(fmt.Sprintf("idempotency: Options.TTL must be in [%s, %s], got %s", MinTTL, MaxTTL, opts.TTL))
	}
	if opts.Hash == nil {
		opts.Hash = CanonicalJSONSHA256
	}
	if opts.Scope == nil {
		opts.Scope = PrincipalScope
	}
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	if opts.ClockSkew < 0 {
		panic("idempotency: Options.ClockSkew must be non-negative")
	}
	if opts.ClockSkew == 0 {
		opts.ClockSkew = DefaultClockSkew
	}
	required := true
	if opts.KeyRequired != nil {
		required = *opts.KeyRequired
	}
	return &Store{opts: opts, keyRequired: required}
}

// TTL returns the configured replay window.
func (s *Store) TTL() time.Duration { return s.opts.TTL }

// Capability returns the capabilities fragment for this store. Sellers MUST
// merge this under capabilities.adcp.idempotency in get_adcp_capabilities.
// Use MergeCapability for a helper that wires it at the correct nesting.
func (s *Store) Capability() map[string]any {
	return map[string]any{
		"replay_ttl_seconds": int64(s.opts.TTL.Seconds()),
	}
}

// MergeCapability inserts this store's capability fragment into a capabilities
// map at the correct nesting path (caps.adcp.idempotency). Safe to call on an
// empty map.
func (s *Store) MergeCapability(caps map[string]any) {
	adcp, ok := caps["adcp"].(map[string]any)
	if !ok {
		adcp = map[string]any{}
		caps["adcp"] = adcp
	}
	adcp["idempotency"] = s.Capability()
}

// Handler is the business handler signature the middleware wraps. Req is the
// raw request JSON bytes; resp is the inner response payload (NOT the
// envelope — the caller wraps this with `replayed: …` at response time).
//
// Contract: returning a nil error caches resp as-is. Task-level failures that
// should NOT be cached (so a retry can re-execute) MUST be returned as a Go
// error. The middleware cannot distinguish a "success" envelope from a
// "failed" envelope hidden inside resp.
type Handler func(ctx context.Context, req []byte) (resp []byte, err error)

// Result is the outcome of a wrapped call. Callers read Replayed to set the
// envelope flag. Key is empty when the caller opted out via KeyRequired=false
// and the request omitted idempotency_key.
type Result struct {
	Response []byte
	Replayed bool
	Key      string
}

// Wrap composes a handler with the idempotency middleware. The returned
// function expects a raw JSON request containing an `idempotency_key` field.
func (s *Store) Wrap(h Handler) func(ctx context.Context, req []byte) (*Result, error) {
	return func(ctx context.Context, req []byte) (*Result, error) {
		key, err := extractKey(req)
		if err != nil {
			return nil, err
		}
		if key == "" {
			if s.keyRequired {
				return nil, &MissingKeyError{}
			}
			resp, err := h(ctx, req)
			if err != nil {
				return nil, err
			}
			return &Result{Response: resp}, nil
		}
		if err := Validate(key); err != nil {
			return nil, err
		}

		scope, err := s.opts.Scope(ctx, req)
		if err != nil {
			return nil, err
		}

		hash, err := s.opts.Hash(req)
		if err != nil {
			return nil, err
		}

		now := s.opts.Clock()

		if existing, err := s.opts.Backend.Get(ctx, scope, key); err != nil {
			return nil, err
		} else if existing != nil {
			return s.evaluateExisting(existing, hash, key, now)
		}

		resp, err := h(ctx, req)
		if err != nil {
			return nil, err
		}

		entry := &Entry{
			Hash:      hash,
			Response:  resp,
			CreatedAt: now,
			ExpiresAt: now.Add(s.opts.TTL),
		}
		winner, stored, err := s.opts.Backend.PutIfAbsent(ctx, scope, key, entry)
		if err != nil {
			return nil, err
		}
		if stored {
			return &Result{Response: resp, Replayed: false, Key: key}, nil
		}
		if winner == nil {
			// Backend reported conflict but the conflicting row vanished
			// (sweeper or TTL). Return the freshly-computed response as
			// non-replayed rather than erroring; the caller still gets a
			// correct result.
			return &Result{Response: resp, Replayed: false, Key: key}, nil
		}
		return s.evaluateExisting(winner, hash, key, now)
	}
}

// evaluateExisting applies TTL (with clock skew) and hash-match rules to a
// stored entry and returns the replay result or a typed error.
func (s *Store) evaluateExisting(existing *Entry, hash, key string, now time.Time) (*Result, error) {
	if !existing.ExpiresAt.IsZero() && now.After(existing.ExpiresAt.Add(s.opts.ClockSkew)) {
		return nil, &ExpiredError{Key: key}
	}
	if existing.Hash != hash {
		return nil, &ConflictError{Key: key}
	}
	return &Result{Response: existing.Response, Replayed: true, Key: key}, nil
}

// extractKey reads idempotency_key from the top level of a JSON request.
func extractKey(req []byte) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req, &m); err != nil {
		return "", fmt.Errorf("idempotency: decode request: %w", err)
	}
	raw, ok := m["idempotency_key"]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &InvalidKeyError{Reason: "not a string"}
	}
	return s, nil
}

// ---- principal context ----

type principalKey struct{}

// WithPrincipal attaches a principal identifier to ctx for PrincipalScope.
// Callers typically set this after authenticating the request and before
// invoking the wrapped handler.
func WithPrincipal(ctx context.Context, principalID string) context.Context {
	return context.WithValue(ctx, principalKey{}, principalID)
}

// PrincipalFromContext returns the principal previously set via WithPrincipal,
// or "" if none.
func PrincipalFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(principalKey{}).(string); ok {
		return v
	}
	return ""
}

// PrincipalScope is the default ScopeFn. It requires a principal in context —
// unscoped keys would let one caller observe another caller's cached responses.
func PrincipalScope(ctx context.Context, _ []byte) (string, error) {
	p := PrincipalFromContext(ctx)
	if p == "" {
		return "", errors.New("idempotency: principal missing from context; call WithPrincipal before invoking the wrapped handler")
	}
	return "principal:" + p, nil
}

// SessionScope scopes keys to (principal, session). Use for si_send_message,
// where the natural unit is a logical turn within a session.
//
// SECURITY: the session id is read from the payload field identified by
// sessionIDField. Callers MUST validate that the session id matches the
// authenticated session (typically by rejecting requests where payload
// session_id disagrees with the transport-layer session) before invoking the
// wrapped handler. Without that check, a principal could cross-scope into
// another session they own by submitting its id in the payload.
func SessionScope(sessionIDField string) ScopeFn {
	return func(ctx context.Context, payload []byte) (string, error) {
		principal := PrincipalFromContext(ctx)
		if principal == "" {
			return "", errors.New("idempotency: principal missing from context")
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(payload, &m); err != nil {
			return "", fmt.Errorf("idempotency: decode request for session scope: %w", err)
		}
		raw, ok := m[sessionIDField]
		if !ok {
			return "", fmt.Errorf("idempotency: session id field %q missing", sessionIDField)
		}
		var sid string
		if err := json.Unmarshal(raw, &sid); err != nil {
			return "", fmt.Errorf("idempotency: session id field %q is not a string", sessionIDField)
		}
		if sid == "" {
			return "", fmt.Errorf("idempotency: session id field %q is empty", sessionIDField)
		}
		return "principal:" + principal + ":session:" + sid, nil
	}
}
