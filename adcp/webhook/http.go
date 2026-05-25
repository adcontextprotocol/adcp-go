package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/adcontextprotocol/adcp-go/adcp/idempotency"
	"github.com/adcontextprotocol/adcp-go/adcp/signing"
)

// DefaultMaxBodyBytes caps the body read by HTTPHandler. 1 MiB comfortably
// holds every AdCP webhook payload schema; larger bodies usually indicate an
// attack or a misconfigured sender batching into a single event.
const DefaultMaxBodyBytes int64 = 1 << 20

// HTTPHandlerOptions configures HTTPHandler.
type HTTPHandlerOptions struct {
	// Store dedupes inbound deliveries. Required.
	Store *Store

	// Handler processes first-delivery events. Required.
	Handler Handler

	// Verification, when non-nil, wraps the dedup handler in an RFC 9421
	// verifier pre-configured for ProfileWebhookSigning + DigestRequired
	// (adcontextprotocol/adcp#2423). Unsigned or wrong-profile deliveries are
	// rejected with webhook_signature_* codes before reaching the dedup layer.
	//
	// Leaving Verification nil is correct only when an outer middleware
	// already verifies signatures OR when the deployment uses the legacy
	// HMAC fallback (deprecated, removed in AdCP 4.0). In that case the
	// caller MUST set AllowUnverified=true AND supply a Sender that derives
	// the authenticated sender identity from the outer auth — otherwise
	// HTTPHandler panics at construction time.
	Verification *VerificationOptions

	// AllowUnverified is the explicit opt-in for running HTTPHandler without
	// signature verification. HTTPHandler panics when Verification is nil
	// and AllowUnverified is false, to block the accidental "custom Sender
	// + no verification = attacker-controlled scope" misconfiguration. When
	// true, Sender MUST also be non-nil; SignerSender is rejected because
	// it cannot derive identity without Verification.
	AllowUnverified bool

	// Sender derives the authenticated sender identity from the request.
	// Defaults to SignerSender, which reads the RFC 9421 VerifiedSigner set
	// either by the embedded Verification middleware or by an outer
	// signing.Middleware. Callers authenticating via HMAC or Bearer should
	// supply a resolver that reads the identity their auth middleware
	// attached to the context.
	//
	// Returning ("", nil) is treated as unauthenticated and the request is
	// rejected with 401. Returning an error is treated the same.
	Sender func(r *http.Request) (string, error)

	// MaxBodyBytes caps the body read. Zero selects DefaultMaxBodyBytes.
	MaxBodyBytes int64

	// Logger is used for warn/error events. Defaults to slog.Default.
	Logger *slog.Logger
}

// VerificationOptions configures the embedded webhook-signing verifier.
// Mirrors the subset of signing.MiddlewareOptions relevant to webhooks —
// Profile is fixed to ProfileWebhookSigning and ContentDigestPolicy to
// DigestRequired per adcontextprotocol/adcp#2423.
type VerificationOptions struct {
	// Resolver resolves keyid → JWK. Required.
	Resolver signing.JWKSResolver

	// Replay deduplicates (keyid, nonce) pairs. Required.
	Replay signing.ReplayStore

	// Revocation reports per-keyid revocation state. Passing nil skips
	// revocation checks — acceptable for dev/test, unsafe for production.
	Revocation signing.RevocationSource

	// MaxBodyBytes caps the body buffered for content-digest recompute.
	// 0 selects the signing package default (32 MiB).
	MaxBodyBytes int64

	// SchemeOverride forces a URL scheme for signature base reconstruction
	// behind TLS-terminating proxies. See signing.MiddlewareOptions.SchemeOverride.
	SchemeOverride string
}

// HTTPHandler returns an http.Handler that reads the webhook body, dedupes on
// idempotency_key, and dispatches first deliveries to the configured Handler.
// Compose with signing.Middleware to add RFC 9421 signature verification.
//
// Status codes:
//   - 200 — first delivery handled successfully, OR replay of a stored key
//   - 400 — missing / malformed idempotency_key, malformed body
//   - 401 — sender identity could not be resolved
//   - 409 — idempotency_key reused with a different payload (sender bug)
//   - 410 — idempotency_key is valid but past the dedup window
//   - 413 — body exceeds MaxBodyBytes
//   - 500 — Handler returned an error; sender should retry
func HTTPHandler(opts HTTPHandlerOptions) http.Handler {
	if opts.Store == nil {
		panic("webhook: HTTPHandlerOptions.Store is required")
	}
	if opts.Handler == nil {
		panic("webhook: HTTPHandlerOptions.Handler is required")
	}
	if opts.Verification == nil && !opts.AllowUnverified {
		panic("webhook: HTTPHandlerOptions.Verification is required, or set AllowUnverified=true and provide a custom Sender (legacy HMAC fallback, removed in AdCP 4.0)")
	}
	if opts.Verification == nil && opts.AllowUnverified && opts.Sender == nil {
		panic("webhook: HTTPHandlerOptions.Sender is required when AllowUnverified=true — SignerSender cannot derive identity without verification")
	}
	senderFn := opts.Sender
	if senderFn == nil {
		senderFn = SignerSender
	}
	max := opts.MaxBodyBytes
	if max <= 0 {
		max = DefaultMaxBodyBytes
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		senderID, err := senderFn(r)
		if err != nil || senderID == "" {
			if err != nil {
				logger.Warn("webhook: sender resolution failed", "err", err)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, max+1))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > max {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		ctx := WithSender(r.Context(), senderID)
		res, err := opts.Store.Dedup(ctx, body, opts.Handler)
		if err != nil {
			writeError(w, logger, err)
			return
		}
		if res.Replayed {
			logger.Debug("webhook: replayed", "key", idempotency.LogKey(res.Key), "sender", senderID)
		}
		w.WriteHeader(http.StatusOK)
	})

	if opts.Verification != nil {
		if opts.Verification.Resolver == nil {
			panic("webhook: VerificationOptions.Resolver is required")
		}
		if opts.Verification.Replay == nil {
			panic("webhook: VerificationOptions.Replay is required")
		}
		// Every webhook delivery MUST be signed in AdCP 3.0
		// (adcontextprotocol/adcp#2423). We model this by naming a synthetic
		// operation that matches RequiredFor so unsigned deliveries are
		// rejected with webhook_signature_required rather than falling
		// through to the Sender-derived 401. The op name is internal-only
		// (never surfaces on the wire).
		const webhookOp = "webhook"
		// Propagate the outer body cap to the signing layer so a tight
		// HTTPHandlerOptions.MaxBodyBytes isn't silently undermined by the
		// signing middleware's 32 MiB default when computing content-digest.
		verifyMax := opts.Verification.MaxBodyBytes
		if verifyMax == 0 {
			verifyMax = max
		}
		mw := signing.Middleware(signing.MiddlewareOptions{
			Profile:             signing.ProfileWebhookSigning,
			Resolver:            opts.Verification.Resolver,
			Replay:              opts.Verification.Replay,
			Revocation:          opts.Verification.Revocation,
			OperationResolver:   func(_ *http.Request) string { return webhookOp },
			RequiredFor:         []string{webhookOp},
			ContentDigestPolicy: signing.DigestRequired,
			MaxBodyBytes:        verifyMax,
			SchemeOverride:      opts.Verification.SchemeOverride,
			Logger:              logger,
		})
		handler = mw(handler)
	}
	return handler
}

// SignerSender derives a sender identity from the RFC 9421 VerifiedSigner
// attached by signing.Middleware. Returns "" if the request is unsigned —
// HTTPHandler treats that as unauthenticated.
func SignerSender(r *http.Request) (string, error) {
	v := signing.VerifiedSignerFromContext(r.Context())
	if v == nil {
		return "", nil
	}
	return v.KeyID, nil
}

// NewSigner constructs a signing.Signer pinned to ProfileWebhookSigning.
// Publishers SHOULD use separate keys (and separate NewSigner instances) for
// request signing and webhook signing — a key scoped for one profile is
// rejected by the other profile's verifier per the step 8 adcp_use check.
func NewSigner(opts signing.SignerOptions) (*signing.Signer, error) {
	opts.Profile = signing.ProfileWebhookSigning
	return signing.NewSigner(opts)
}

// DeliverResult bundles the outputs of Deliver. Callers retry by calling
// Deliver again with the same payload struct and reusing Body byte-identical —
// the signature is re-minted on each call (fresh timestamp + nonce), but
// IdempotencyKey remains stable because Marshal preserves an existing field.
type DeliverResult struct {
	// Response is the HTTP response from the subscriber. Callers MUST close
	// Response.Body.
	Response *http.Response

	// Body is the frozen wire bytes. Retries MUST resend these bytes exactly
	// — RFC 9421 Content-Digest re-verification is byte-exact, so a
	// re-marshal on retry would invalidate the digest even when the logical
	// payload is unchanged.
	Body []byte

	// IdempotencyKey is the key from the payload (generated by Marshal when
	// the field was empty). Useful for logs and sender-side retry bookkeeping.
	IdempotencyKey string
}

// Deliver marshals p, signs the HTTP request with signer (content-digest
// REQUIRED per adcontextprotocol/adcp#2423), and POSTs to url.
//
// Deliver does NOT retry. Senders retry by calling Deliver again with the
// same payload struct — Marshal preserves the existing IdempotencyKey, so
// the receiver dedupes on the same key while the signature is re-minted with
// a fresh timestamp + nonce.
//
// If client is nil, http.DefaultClient is used. Signed requests MUST NOT
// follow HTTP redirects (the signature binds @target-uri); Deliver shallow-
// clones the provided client and installs CheckRedirect =
// http.ErrUseLastResponse when the caller left it unset. The caller's client
// is not mutated.
func Deliver(ctx context.Context, url string, p Payload, signer *signing.Signer, client *http.Client) (*DeliverResult, error) {
	if signer == nil {
		return nil, fmt.Errorf("webhook: signer is required")
	}
	body, key, err := Marshal(p)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}); err != nil {
		return nil, fmt.Errorf("webhook: sign request: %w", err)
	}
	effective := clientNoRedirect(client)
	// Ownership of resp.Body is returned to the caller in DeliverResult.
	//nolint:bodyclose
	resp, err := effective.Do(req)
	if err != nil {
		return nil, err
	}
	return &DeliverResult{Response: resp, Body: body, IdempotencyKey: key}, nil
}

// clientNoRedirect returns a client that does not follow redirects. If src is
// nil a fresh client is returned; otherwise src is shallow-cloned so the
// caller's CheckRedirect is preserved if set, or installed when absent.
func clientNoRedirect(src *http.Client) *http.Client {
	noFollow := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if src == nil {
		return &http.Client{CheckRedirect: noFollow}
	}
	if src.CheckRedirect != nil {
		return src
	}
	clone := *src
	clone.CheckRedirect = noFollow
	return &clone
}

func writeError(w http.ResponseWriter, logger *slog.Logger, err error) {
	var (
		conflict *idempotency.ConflictError
		expired  *idempotency.ExpiredError
		missing  *idempotency.MissingKeyError
		invalid  *idempotency.InvalidKeyError
		syntax   *json.SyntaxError
	)
	switch {
	case errors.As(err, &conflict):
		logger.Warn("webhook: idempotency_key reused with different payload", "key", idempotency.LogKey(conflict.Key))
		http.Error(w, "idempotency_key conflict", http.StatusConflict)
	case errors.As(err, &expired):
		http.Error(w, "idempotency_key expired", http.StatusGone)
	case errors.As(err, &missing):
		http.Error(w, "missing idempotency_key", http.StatusBadRequest)
	case errors.As(err, &invalid):
		http.Error(w, "invalid idempotency_key", http.StatusBadRequest)
	case errors.As(err, &syntax):
		// Malformed JSON body — sender bug, not a receiver fault. 400 so
		// senders stop retrying a permanently-broken payload.
		http.Error(w, "malformed JSON body", http.StatusBadRequest)
	default:
		logger.Error("webhook: handler failed", "err", err)
		http.Error(w, "webhook handler failed", http.StatusInternalServerError)
	}
}
