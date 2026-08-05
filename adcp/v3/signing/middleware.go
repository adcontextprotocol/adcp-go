package signing

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// contextKey is the private context key type used to attach VerifiedSigner
// to a request context.
type contextKey struct{}

// VerifiedSignerFromContext returns the VerifiedSigner attached to ctx by the
// middleware. Returns nil if the request was not signed (or signing was not
// required and the request had no signature).
func VerifiedSignerFromContext(ctx context.Context) *VerifiedSigner {
	v, _ := ctx.Value(contextKey{}).(*VerifiedSigner)
	return v
}

// OperationResolver returns the AdCP protocol operation name for a given
// request. Typical implementations inspect the request path or the MCP/A2A
// tool/skill name derived from the transport envelope.
//
// Returning "" disables RequiredFor enforcement for that request (unsigned
// requests pass through). Middleware callers using the /adcp/<operation>
// convention can use DefaultOperationResolver.
type OperationResolver func(*http.Request) string

// DefaultOperationResolver derives the AdCP operation name from the last
// segment of the request path (e.g. /adcp/create_media_buy → create_media_buy).
// Returns "" if the path does not match /adcp/<op>.
func DefaultOperationResolver(r *http.Request) string {
	p := r.URL.Path
	const prefix = "/adcp/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	op := p[len(prefix):]
	if i := strings.IndexByte(op, '/'); i >= 0 {
		op = op[:i]
	}
	return op
}

// MiddlewareOptions configures the HTTP middleware.
type MiddlewareOptions struct {
	// Profile selects the signing profile accepted by this middleware. Zero
	// value is ProfileRequestSigning. Webhook receivers MUST set
	// ProfileWebhookSigning so a request-signing signature presented to a
	// webhook endpoint is rejected with webhook_signature_tag_invalid.
	Profile Profile

	// Resolver resolves keyid to a JWK and the signing agent's URL.
	// Typical implementations: StaticJWKSResolver for tests / specialized
	// deployments; HTTPJWKSResolver for production with brand.json-derived
	// agent mappings.
	Resolver JWKSResolver

	// Replay deduplicates (keyid, nonce) pairs. Defaults to a fresh
	// NewMemoryReplayStore(0) when nil — appropriate for single-instance
	// verifiers; distributed deployments SHOULD supply a shared backing
	// store (Redis, etc.) implementing ReplayStore with atomic cap+insert
	// semantics at step 13. Defaulting to nil silently disabled the replay
	// check, the exact security regression AdCP verifier checklist step 13
	// exists to prevent.
	Replay ReplayStore

	// Revocation reports per-keyid revocation state and whether the verifier's
	// cached list is stale. Passing nil skips revocation checks — acceptable
	// for dev/test, unsafe for production; the middleware logs a warning
	// at construction time when RequiredFor is non-empty and Revocation is nil.
	Revocation RevocationSource

	// OperationResolver returns the AdCP operation name for a request. Used
	// to check RequiredFor at the pre-check step. Supply DefaultOperationResolver
	// for the /adcp/<op> path convention.
	OperationResolver OperationResolver

	// RequiredFor lists operations whose unsigned requests must be rejected
	// with request_signature_required. Empty by default (AdCP 3.0 opt-in);
	// AdCP 4.0 normatively requires spend-committing operations to appear here.
	RequiredFor []string

	// ContentDigestPolicy controls whether signers MUST, MUST NOT, or MAY
	// cover content-digest. Defaults to DigestEither.
	ContentDigestPolicy DigestPolicy

	// MaxBodyBytes caps the body buffered for content-digest recompute
	// (step 11). 0 selects a 32 MiB default. Set lower if your upstream
	// already enforces a tighter body-size limit.
	MaxBodyBytes int64

	// OnReject, if non-nil, is called instead of the default 401 response on
	// verification failure. Useful for MCP / JSON-RPC servers that need to
	// shape the error as a protocol-specific envelope. The handler MUST write
	// both a status code and a body.
	//
	// The default behavior is 401 Unauthorized with WWW-Authenticate set to
	// `Signature error="<code>"` and a text/plain "unauthorized" body.
	OnReject func(w http.ResponseWriter, r *http.Request, e *Error)

	// Logger is used to log verification failures server-side. If nil,
	// slog.Default() is used. The middleware logs the error code, detail,
	// operation name, and (when known) keyid — enough to trace "which
	// counterparty is sending broken signatures."
	Logger *slog.Logger

	// SchemeOverride forces a specific URL scheme (https/http) for signature
	// base reconstruction. Most deployments should leave this empty; the
	// middleware derives "https" for TLS requests and "http" otherwise.
	// Set this when your reverse proxy terminates TLS and the verifier sees
	// plain HTTP.
	SchemeOverride string
}

// Middleware returns an http.Handler middleware that verifies incoming AdCP
// request signatures per the profile.
//
// On success, the VerifiedSigner is attached to the request context and next
// is invoked.
//
// On failure, the middleware writes 401 Unauthorized with the
// `WWW-Authenticate: Signature error="<code>"` header (no realm, per spec) and
// a text/plain "unauthorized" body. Callers that need a protocol-specific
// error body (e.g. JSON-RPC-shaped MCP error) should wrap this middleware and
// translate the 401 into the outer protocol's error format.
//
// For requests that are not signed AND not in RequiredFor, the middleware
// invokes next with no signer context — callers that want to enforce signing
// universally should populate RequiredFor.
func Middleware(opts MiddlewareOptions) func(http.Handler) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	profile := opts.Profile
	if profile.Tag == "" {
		profile = ProfileRequestSigning
	}
	// Construct the default replay store once at wire-up so every request
	// served by this middleware shares the same dedup state — lazy
	// per-request construction would defeat replay detection entirely.
	// Pass an explicit shared store (Redis, etc.) for multi-replica
	// deployments where replay state must be coordinated across processes.
	replay := opts.Replay
	if replay == nil {
		replay = NewMemoryReplayStore(0)
	}
	if len(opts.RequiredFor) > 0 && opts.Revocation == nil {
		logger.Warn("signing.Middleware: RequiredFor is non-empty but Revocation is nil — verifier will not enforce key revocation")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			opName := ""
			if opts.OperationResolver != nil {
				opName = opts.OperationResolver(r)
			}
			scheme := opts.SchemeOverride
			if scheme == "" {
				if r.TLS != nil {
					scheme = "https"
				} else {
					scheme = "http"
				}
			}
			signer, err := VerifyRequestSignature(r, VerifyOptions{
				OperationName:       opName,
				RequiredFor:         opts.RequiredFor,
				Profile:             profile,
				ContentDigestPolicy: opts.ContentDigestPolicy,
				Scheme:              scheme,
				Resolver:            opts.Resolver,
				Revocation:          opts.Revocation,
				Replay:              replay,
				MaxBodyBytes:        opts.MaxBodyBytes,
			})
			if err != nil {
				e := AsError(err)
				if e == nil {
					e = wrapError(CodeInvalid, "untyped verifier error", err)
				}
				// Best-effort keyid extraction for logs — helps operators
				// trace "which buyer is sending broken signatures."
				keyid := ""
				if parsed, perr := parseSignatureInput(r.Header.Get(signatureInputHeader)); perr == nil {
					keyid = parsed.keyID
				}
				logger.Warn("signature rejected",
					"code", e.WireCode(profile),
					"detail", e.Detail,
					"op", opName,
					"keyid", keyid,
					"profile", profile.Tag,
				)
				if opts.OnReject != nil {
					opts.OnReject(w, r, e)
					return
				}
				w.Header().Set("WWW-Authenticate", `Signature error="`+e.WireCode(profile)+`"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if signer != nil {
				ctx := context.WithValue(r.Context(), contextKey{}, signer)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}
