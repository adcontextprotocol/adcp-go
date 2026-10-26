package signing

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/strictjson"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// contextKey is the private context key type used to attach VerifiedSigner
// to a request context.
type contextKey struct{}

// VerifiedSignerFromContext returns the VerifiedSigner attached to ctx by the
// middleware. Returns nil if the request was not signed (or signing was not
// required and the request had no signature).
//
// Inside MCP tool handlers use VerifiedSignerFromToolRequest instead: a
// stateful go-sdk session runs handlers with the context of its initialize
// request, so this can return a different, earlier request's signer.
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

	// ProtocolMethodsRequiredFor lists JSON-RPC protocol method names whose
	// unsigned requests must be rejected. It is distinct from RequiredFor,
	// which contains AdCP tool-operation names.
	ProtocolMethodsRequiredFor []string

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

	// ObserveOnly puts this middleware instance in shadow mode. Verification
	// still runs, but a request that fails it — including an unsigned
	// request to an operation in RequiredFor — is NOT rejected: next is
	// invoked with no VerifiedSigner in the context, exactly as if the
	// request had arrived unsigned. The failure is logged at INFO level
	// (instead of the usual WARN + 401) via Logger, so operators can watch
	// the failure rate before promoting the operation to full enforcement.
	//
	// This maps to the spec's `warn_for` rollout stop — the shadow-mode
	// bridge between `supported_for` (verified when present, never required)
	// and `required_for` (verified and mandatory). Wire a dedicated
	// Middleware instance with ObserveOnly=true for the operations you've
	// moved to `warn_for`; RequiredFor should normally be empty on that
	// instance, since `warn_for` and `required_for` are disjoint per spec —
	// see https://adcontextprotocol.org/docs/building/implementation/security#transport-capability-advertisement.
	//
	// One case still hard-rejects even under ObserveOnly: a partial or
	// malformed Signature/Signature-Input header pair (one header present
	// without the other, or either header present but unparseable) —
	// surfaced as *Error{Code: CodeHeaderMalformed}. The spec requires this
	// because a broken pair "cannot be safely interpreted as either signed
	// or unsigned traffic"; a well-formed signature that merely fails
	// verification (bad crypto, unknown key, expired window, replay, ...) is
	// the case ObserveOnly is for and passes through.
	//
	// ObserveOnly covers the RFC 9421 path and unsigned requests only. A
	// request verified under Web Bot Auth that fails is always rejected;
	// with WebBotAuthOptions.Exclusive that includes every signed request,
	// an RFC 9421 signature too, even on an operation that would pass
	// unsigned. Only an unsigned request to an operation in RequiredFor is
	// let through.
	ObserveOnly bool

	// WebBotAuth, when non-nil, enables the experimental AdCP 3.3 Web Bot
	// Auth profile on this middleware alongside the RFC 9421 profile (or
	// instead of it, with WebBotAuthOptions.Exclusive). A request carrying
	// Signature-Agent or a web-bot-auth tag is then verified under Web Bot
	// Auth only and never falls back to the RFC 9421 profile, including
	// under ObserveOnly. Failures are answered with the profile's statuses
	// (400; 403 with Accept-Signature; 403 without it for a revoked key; 429
	// with Accept-Signature for a reused signature or nonce), not
	// request_signature_* codes. A non-empty Web Bot Auth signed body must
	// be strict JSON (no duplicate or case-folded duplicate keys) or it is
	// rejected with 400.
	//
	// An unsigned request to an operation in RequiredFor or
	// ProtocolMethodsRequiredFor is answered as the profile page says for a
	// verifier that also advertises the RFC 9421 profile: 401 with
	// request_signature_required, plus Accept-Signature for Web Bot Auth.
	// With Exclusive it is 403 with Accept-Signature.
	//
	// When nil, Web Bot Auth requests take the RFC 9421 path as before, and
	// the middleware logs at INFO that the RFC 9421 profile is deprecated
	// in 3.3.
	WebBotAuth *WebBotAuthOptions

	// BoundOrigins enforces "no downgrade": once an origin is bound to a
	// principal under Web Bot Auth, an RFC 9421 signature from any agent URL
	// at that origin is refused with PERMISSION_DENIED. Nil disables the
	// check.
	BoundOrigins OriginBindingLookup
}

// WebBotAuthOptions configures the Web Bot Auth profile on Middleware.
// EXPERIMENTAL (AdCP 3.3).
type WebBotAuthOptions struct {
	// Exclusive makes Web Bot Auth the only request-signing profile this
	// middleware accepts, for a verifier whose request_signing.profiles
	// advertises only "web-bot-auth". Every signed request is verified
	// under Web Bot Auth, so an RFC 9421 signature without a web-bot-auth
	// member is refused with 403 and Accept-Signature, and
	// MiddlewareOptions.Resolver, BoundOrigins, and the RFC 9421 profile
	// are unused. An unsigned request to an operation in RequiredFor or
	// ProtocolMethodsRequiredFor, or one whose body carries
	// push_notification_config.authentication, is refused with 403 and
	// Accept-Signature, not 401 request_signature_required. Leave it false
	// when the verifier also advertises "adcp/request-signing/v1".
	Exclusive bool

	// Keys resolves (origin, thumbprint). Required; NewDirectoryResolver is
	// the production resolver.
	Keys WBAKeyResolver

	// Revocation checks each verified key against its origin's list, in
	// the authorization layer after verification (CheckWBARevocation);
	// NewOriginRevocationChecker is the production checker. A revoked key
	// is refused with 403 and no Accept-Signature. Every other state is
	// recorded in VerifiedSigner.Revocation for the Authorizer:
	// identity.BindingGate treats an origin with no list, or no checker, as
	// identity-established, fails spend-committing calls closed with
	// SERVICE_UNAVAILABLE while the list is stale, and fails every bound
	// call with SERVICE_UNAVAILABLE while the revocation store is
	// unavailable. Nil skips the check, so
	// no origin can be bound.
	Revocation WBARevocationChecker

	// OnDiscoveryFailure is the local policy for a request whose signer's
	// key directory cannot be resolved (and no unexpired cached copy holds
	// the key). Such a request is unverified (WBA §6.10). The default,
	// DiscoveryFailureReject, answers 403 with Accept-Signature;
	// DiscoveryFailureAnonymous serves it exactly as an anonymous request,
	// except that an operation in RequiredFor or ProtocolMethodsRequiredFor
	// is still refused.
	OnDiscoveryFailure DiscoveryFailurePolicy

	// Authorizer decides authority for a verified origin and returns its
	// bound principal ID, or "" for an identity-established signer. It runs
	// after verification and is where a binding is re-evaluated against the
	// operator's trust record; the signature path itself never reads one.
	// Nil treats every verified origin as unbound.
	Authorizer WBAAuthorizer

	// UnboundFor lists the operations (AdCP tool names or JSON-RPC methods)
	// an identity-established signer may call with its verified identity
	// attached. Any other operation is served exactly as an anonymous
	// request (no verified signer in context), so the caller gets what an
	// anonymous caller gets; an operation in RequiredFor or
	// ProtocolMethodsRequiredFor is refused with PERMISSION_DENIED instead.
	// List get_principal and anything else that needs the identity. Defaults to
	// get_adcp_capabilities and get_principal. The MCP session methods
	// (initialize, notifications/initialized, ping, tools/list) and bodyless
	// streamable-HTTP GET and DELETE requests are always allowed, whatever
	// this lists. Add sync_principal only when its handler implements an
	// explicit open onboarding policy.
	UnboundFor []string

	// OnReject, if non-nil, writes the response for a Web Bot Auth failure.
	// It must write a status and a body, and must not echo e.Reason.
	OnReject func(w http.ResponseWriter, r *http.Request, e *WBAError)

	// Clock supplies the verifier's wall clock. Defaults to time.Now.
	Clock func() time.Time

	// Authorities lists the host[:port] values this endpoint serves; see
	// WBAVerifyOptions.Authorities. Recommended.
	Authorities []string
}

// DiscoveryFailurePolicy is WebBotAuthOptions.OnDiscoveryFailure.
type DiscoveryFailurePolicy int

const (
	// DiscoveryFailureReject answers an unverified request 403 with
	// Accept-Signature. The default.
	DiscoveryFailureReject DiscoveryFailurePolicy = iota
	// DiscoveryFailureAnonymous serves an unverified request as anonymous.
	DiscoveryFailureAnonymous
)

// WBAAuthorizer is the binding hook for verified Web Bot Auth origins. It
// returns the principal bound to signer.AgentURL, re-evaluating the binding
// as its freshness rules require, or "" when the origin is not bound or no
// longer listed (identity-established). A non-nil error is an
// authorization outcome: return NewAuthorizationError with an AdCP code
// (SERVICE_UNAVAILABLE for a retryable outage); any other error is answered
// with PERMISSION_DENIED. operation lets it fail closed only for
// spend-committing calls.
type WBAAuthorizer interface {
	AuthorizeWBA(ctx context.Context, signer *VerifiedSigner, operation string) (principalID string, err error)
}

// OriginBindingLookup reports the principal bound to a canonical origin.
type OriginBindingLookup interface {
	PrincipalForOrigin(ctx context.Context, origin string) (principalID string, bound bool, err error)
}

// rfc9421Deprecation is logged by each Middleware built without WebBotAuth.
const rfc9421Deprecation = "signing.Middleware: the AdCP RFC 9421 request-signing profile is deprecated in AdCP 3.3; " +
	"it remains supported through 3.x and is removed in 4.0. Set MiddlewareOptions.WebBotAuth to also accept the Web Bot Auth profile."

// defaultUnboundFor is what an identity-established signer may call when
// WebBotAuthOptions.UnboundFor is nil.
var defaultUnboundFor = []string{"get_adcp_capabilities", "get_principal"}

// mcpSessionMethods are always allowed to an identity-established signer,
// in addition to UnboundFor: without them it cannot open an MCP session to
// reach the operations UnboundFor allows. They invoke no tool.
var mcpSessionMethods = []string{"initialize", "notifications/initialized", "ping", "tools/list"}

// transportOnly reports a request that names no operation and carries no
// body: a streamable-HTTP GET (the server-to-client event stream) or
// DELETE (session close). It invokes nothing, so an identity-established
// signer may make it.
func transportOnly(r *http.Request, op string) bool {
	if op != "" || (r.Method != http.MethodGet && r.Method != http.MethodDelete) {
		return false
	}
	return r.ContentLength <= 0 && (r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0)
}

// Middleware returns an http.Handler middleware that verifies incoming AdCP
// request signatures per the profile.
//
// On success, the VerifiedSigner is attached to the request context and next
// is invoked.
//
// On failure under the RFC 9421 profile, the middleware writes 401
// Unauthorized with the `WWW-Authenticate: Signature error="<code>"` header
// (no realm, per spec) and a text/plain "unauthorized" body. A request
// verified under Web Bot Auth (MiddlewareOptions.WebBotAuth) is answered
// with that profile's statuses instead: 400, 403 or 429, with
// Accept-Signature where the profile calls for it, or an AdCP error
// envelope for an authorization outcome. Callers that need a protocol-specific
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
	wba := opts.WebBotAuth
	if len(opts.RequiredFor) > 0 || len(opts.ProtocolMethodsRequiredFor) > 0 {
		if (wba == nil || !wba.Exclusive) && opts.Revocation == nil {
			logger.Warn("signing.Middleware: RequiredFor is non-empty but Revocation is nil — verifier will not enforce key revocation")
		}
		if wba != nil && wba.Exclusive && wba.Revocation == nil {
			logger.Warn("signing.Middleware: RequiredFor is non-empty but WebBotAuth.Revocation is nil — identity.BindingGate binds no origin, so RequiredFor operations are refused")
		}
	}
	if wba == nil {
		logger.Info(rfc9421Deprecation)
	}
	unboundList := defaultUnboundFor
	if wba != nil && wba.UnboundFor != nil {
		unboundList = wba.UnboundFor
	}
	unboundFor := map[string]struct{}{}
	for _, op := range append(append([]string(nil), unboundList...), mcpSessionMethods...) {
		unboundFor[op] = struct{}{}
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
			if wba != nil && wba.Exclusive && unsignedRequest(r.Header) {
				serveUnsignedWebBotAuthOnly(w, r, next, wba, opName, opts, logger)
				return
			}
			if wba != nil && (wba.Exclusive || UsesWebBotAuth(r.Header)) {
				serveWebBotAuth(w, r, next, wba, unboundFor, replay, scheme, opName, opts, logger)
				return
			}
			signer, err := VerifyRequestSignature(r, VerifyOptions{
				OperationName:              opName,
				RequiredFor:                opts.RequiredFor,
				ProtocolMethodsRequiredFor: opts.ProtocolMethodsRequiredFor,
				Profile:                    profile,
				ContentDigestPolicy:        opts.ContentDigestPolicy,
				Scheme:                     scheme,
				Resolver:                   opts.Resolver,
				Revocation:                 opts.Revocation,
				Replay:                     replay,
				MaxBodyBytes:               opts.MaxBodyBytes,
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
				// ObserveOnly never overrides a malformed header pair — that
				// case cannot be safely treated as unsigned traffic (see the
				// ObserveOnly doc comment).
				observing := opts.ObserveOnly && e.Code != CodeHeaderMalformed
				level := slog.LevelWarn
				msg := "signature rejected"
				if observing {
					level = slog.LevelInfo
					msg = "signature verification failed (ObserveOnly: request allowed through unverified)"
				}
				logger.Log(r.Context(), level, msg,
					"code", e.WireCode(profile),
					"detail", e.Detail,
					"op", opName,
					"keyid", keyid,
					"profile", profile.Tag,
					"observe_only", observing,
				)
				if observing {
					// No VerifiedSigner is attached — downstream sees this
					// exactly as an unsigned request.
					next.ServeHTTP(w, r)
					return
				}
				if wba != nil && e.Code == CodeRequired {
					// The verifier advertises both profiles: the RFC 9421
					// answer, plus Accept-Signature for Web Bot Auth.
					w.Header().Set(acceptSignatureHeader, AcceptSignature(requestHasBody(r, opts.MaxBodyBytes)))
				}
				if opts.OnReject != nil {
					opts.OnReject(w, r, e)
					return
				}
				w.Header().Set("WWW-Authenticate", `Signature error="`+e.WireCode(profile)+`"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if signer != nil && opts.BoundOrigins != nil {
				// No downgrade: a bound origin is refused the RFC 9421 profile.
				if origin, err := CanonicalOrigin(signer.AgentURL); err == nil {
					_, bound, lerr := opts.BoundOrigins.PrincipalForOrigin(r.Context(), origin)
					if lerr != nil || bound {
						logger.Warn("signature rejected", "code", "PERMISSION_DENIED", "detail", "bound origin requires Web Bot Auth", "op", opName, "profile", profile.Tag)
						writeWBAReject(w, r, wbaDeny("bound origin requires Web Bot Auth"), nil)
						return
					}
				}
			}
			if signer != nil {
				ctx := context.WithValue(r.Context(), contextKey{}, signer)
				r = r.WithContext(ctx)
				serveWithToolRequestSigner(w, r, next, signer)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// unsignedRequest reports a request that carries none of the signature
// fields.
func unsignedRequest(h http.Header) bool {
	return len(h.Values(signatureHeader)) == 0 && len(h.Values(signatureInputHeader)) == 0 && len(h.Values(signatureAgentHeader)) == 0
}

// serveUnsignedWebBotAuthOnly handles an unsigned request on a verifier
// that accepts only Web Bot Auth. A request that needs a signature
// (requiresSignature: a required operation or method, a body carrying
// push_notification_config.authentication, or a body that is not strict
// JSON) is refused with 403 and Accept-Signature; anything else proceeds
// unsigned.
func serveUnsignedWebBotAuthOnly(w http.ResponseWriter, r *http.Request, next http.Handler, wba *WebBotAuthOptions,
	opName string, opts MiddlewareOptions, logger *slog.Logger) {
	op := opName
	if op == "" {
		op = jsonRPCOperation(r, opts.MaxBodyBytes)
	}
	if !requiresSignature(r, op, opts) {
		next.ServeHTTP(w, r)
		return
	}
	if opts.ObserveOnly {
		logger.Info("unsigned request to an operation that requires a signature (ObserveOnly: request allowed through)", "op", op, "profile", ProfileTagWebBotAuth)
		next.ServeHTTP(w, r)
		return
	}
	e := wbaFail(http.StatusForbidden, "", "unsigned request to an operation that requires a signature")
	e.noBody = !requestHasBody(r, opts.MaxBodyBytes)
	rejectWBA(w, r, e, op, wba, logger)
}

// serveWebBotAuth verifies a request selected for the Web Bot Auth profile,
// applies the binding gate, and dispatches or rejects it.
func serveWebBotAuth(w http.ResponseWriter, r *http.Request, next http.Handler, wba *WebBotAuthOptions,
	unboundFor map[string]struct{}, replay ReplayStore, scheme, opName string, opts MiddlewareOptions, logger *slog.Logger) {
	caller, err := VerifyWebBotAuthRequest(r, WBAVerifyOptions{
		Keys:         wba.Keys,
		Replay:       replay,
		Scheme:       scheme,
		MaxBodyBytes: opts.MaxBodyBytes,
		Clock:        wba.Clock,
		Authorities:  wba.Authorities,
	})
	op := opName
	if err != nil {
		if IsDiscoveryFailure(err) && wba.OnDiscoveryFailure == DiscoveryFailureAnonymous {
			if op == "" {
				op = jsonRPCOperation(r, opts.MaxBodyBytes)
			}
			if !requiresSignature(r, op, opts) {
				// Unverified (WBA §6.10): local policy serves it as an
				// anonymous request, with no verified signer.
				logger.Info("web bot auth key discovery failed; serving as anonymous", "op", op, "detail", AsWBAError(err).Reason)
				next.ServeHTTP(w, r)
				return
			}
		}
		rejectWBA(w, r, err, op, wba, logger)
		return
	}
	if op == "" {
		op = jsonRPCOperation(r, opts.MaxBodyBytes)
	}
	// Identity is verified. The authorization layer follows: revocation,
	// then the binding. A revoked key is answered like a signature failure
	// (403, generic body) but without Accept-Signature; every other outcome
	// is the AdCP error envelope with existing codes, never 403 with
	// Accept-Signature.
	if wba.Revocation != nil {
		if err := CheckWBARevocation(r.Context(), wba.Revocation, caller); err != nil {
			rejectWBA(w, r, err, op, wba, logger)
			return
		}
	}
	principal := ""
	if wba.Authorizer != nil {
		principal, err = wba.Authorizer.AuthorizeWBA(r.Context(), caller, op)
		if err != nil {
			if we := AsWBAError(err); we == nil || !we.Authorization {
				err = NewAuthorizationError(CodePermissionDenied, "signer origin is not authorized", err)
			}
			rejectWBA(w, r, err, op, wba, logger)
			return
		}
	}
	if principal == "" && !transportOnly(r, op) {
		if _, ok := unboundFor[op]; !ok {
			// Outside what an identity-established caller may do: it gets
			// what an anonymous caller gets. An operation the middleware
			// requires a signature for is refused with the gate's
			// authorization error (an anonymous caller would be refused
			// too); anything else proceeds exactly as an anonymous request,
			// with no verified signer, for the application to answer.
			if requiresSignature(r, op, opts) {
				rejectWBA(w, r, wbaDeny("signer identity is not bound"), op, wba, logger)
				return
			}
			logger.Info("web bot auth signer is identity-established; serving as anonymous", "op", op)
			next.ServeHTTP(w, r)
			return
		}
	}
	caller.PrincipalID = principal
	ctx := context.WithValue(r.Context(), contextKey{}, caller)
	serveWithToolRequestSigner(w, r.WithContext(ctx), next, caller)
}

// requiresSignature reports whether a request served without a verified
// signer needs one: op (or the request's exact JSON-RPC method) is in
// RequiredFor or ProtocolMethodsRequiredFor, the body carries
// push_notification_config.authentication (the anti-downgrade rule), or the
// body cannot be read or is not strict JSON, so no check on it can be
// trusted to see what the handler will dispatch.
func requiresSignature(r *http.Request, op string, opts MiddlewareOptions) bool {
	if slices.Contains(opts.RequiredFor, op) {
		return true
	}
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	limit := opts.MaxBodyBytes
	if limit <= 0 {
		limit = defaultMaxBodyBytes
	}
	body, err := readAndReplaceBody(r, limit)
	if err != nil || ambiguousBody(body) {
		return true
	}
	return slices.Contains(opts.ProtocolMethodsRequiredFor, protocolMethodFromJSONRPC(body)) ||
		bodyCarriesPushNotificationAuth(body)
}

func rejectWBA(w http.ResponseWriter, r *http.Request, err error, op string, wba *WebBotAuthOptions, logger *slog.Logger) {
	e := AsWBAError(err)
	if e == nil {
		logger.Error("web bot auth verification error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	logger.Warn("web bot auth request rejected",
		"status", e.Status,
		"code", e.Code,
		"detail", e.Reason,
		"op", op,
		"label", e.Label,
		"authorization", e.Authorization,
		"discovery_failure", e.DiscoveryFailure,
		"revoked", e.Revoked,
		"profile", ProfileTagWebBotAuth,
	)
	writeWBAReject(w, r, e, wba.OnReject)
}

// writeWBAReject writes the profile's failure response, split by layer. A
// signature-path failure is its WBA status and headers (400; 403 with
// Accept-Signature; 403 without it for a revoked key; 429 with
// Accept-Signature for a replay, or with Retry-After for replay-store
// capacity) with the generic authentication error as its body. An authorization outcome
// (identity established) is the standard AdCP error envelope with its code
// and no Accept-Signature: PERMISSION_DENIED as 403, SERVICE_UNAVAILABLE as
// 503 with Retry-After. Neither body carries signature-specific detail.
func writeWBAReject(w http.ResponseWriter, r *http.Request, e *WBAError, onReject func(http.ResponseWriter, *http.Request, *WBAError)) {
	if onReject != nil {
		onReject(w, r, e)
		return
	}
	if e.Authorization {
		code, recovery, message := e.Code, "correctable", "Agent is not authorized for this request"
		if code == "" {
			code = CodePermissionDenied
		}
		status := http.StatusForbidden
		if code == CodeServiceUnavailable {
			status, recovery, message = http.StatusServiceUnavailable, "transient", "Authorization status is temporarily unavailable; retry later"
			w.Header().Set("Retry-After", "60")
		}
		body, _ := json.Marshal(map[string]any{"errors": []map[string]string{{"code": code, "message": message, "recovery": recovery}}})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	for k, vs := range e.ResponseHeader() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(e.Status)
	_, _ = w.Write(genericAuthenticationError)
}

// genericAuthenticationError is the one body for every signature-path
// failure, whatever the cause, so the response is no oracle.
var genericAuthenticationError = []byte(`{"errors":[{"code":"AUTH_INVALID","message":"Request authentication failed","recovery":"correctable"}]}`)

// jsonRPCOperation names the operation of a JSON-RPC request body: the
// tool name of an MCP tools/call, or otherwise the JSON-RPC method (for
// example initialize or tools/list). It reads members by exact,
// case-sensitive name, as the MCP SDK does, and returns "" (no operation,
// so nothing is allowed for an unbound signer) for a body it cannot read
// unambiguously, including one whose keys collide under case folding.
// A2A deployments resolve skills with MiddlewareOptions.OperationResolver.
// The body has already been buffered, and strict-validated, by verification.
func jsonRPCOperation(r *http.Request, maxBody int64) string {
	if r.Body == nil {
		return ""
	}
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	body, err := readAndReplaceBody(r, maxBody)
	if err != nil || strictjson.Validate(body, 0) != nil {
		return ""
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	var version, method string
	if json.Unmarshal(envelope["jsonrpc"], &version) != nil || version != "2.0" ||
		json.Unmarshal(envelope["method"], &method) != nil || method == "" {
		return ""
	}
	if method != "tools/call" {
		return method
	}
	var params map[string]json.RawMessage
	var name string
	if json.Unmarshal(envelope["params"], &params) != nil || json.Unmarshal(params["name"], &name) != nil {
		return ""
	}
	return name
}

// TokenInfoSignerKey is the auth.TokenInfo.Extra key under which Middleware
// carries the verified signer to MCP tool handlers when WebBotAuth is set.
// Read it with VerifiedSignerFromToolRequest.
const TokenInfoSignerKey = "adcp.verified_signer" // #nosec G101 -- a map key, not a credential.

// VerifiedSignerFromToolRequest returns the signer Middleware verified for
// the HTTP request that carried this MCP tool call, or nil. The middleware
// attaches it for every verified signer, under either profile.
//
// Use it, not VerifiedSignerFromContext, inside MCP tool handlers: a
// stateful go-sdk session binds handlers to the context of its initialize
// request, so a context value can belong to a different, earlier request.
// The MCP SDK copies auth.TokenInfo from each HTTP request into
// req.Extra.TokenInfo, and Middleware places the signer there (in
// TokenInfo.Extra, leaving UserID untouched, so session binding and
// principal checks are unchanged). Install Middleware inside (after)
// any bearer-token middleware, which replaces TokenInfo; if the signer is
// lost that way, this returns nil and callers fail closed.
func VerifiedSignerFromToolRequest(req *mcp.CallToolRequest) *VerifiedSigner {
	if req == nil || req.Extra == nil {
		return nil
	}
	return VerifiedSignerFromTokenInfo(req.Extra.TokenInfo)
}

// VerifiedSignerFromTokenInfo returns the signer stored under
// TokenInfoSignerKey, or nil.
func VerifiedSignerFromTokenInfo(ti *auth.TokenInfo) *VerifiedSigner {
	if ti == nil {
		return nil
	}
	s, _ := ti.Extra[TokenInfoSignerKey].(*VerifiedSigner)
	return s
}

// serveWithToolRequestSigner calls next with signer added to the request's
// auth.TokenInfo, which the MCP SDK forwards per HTTP request into
// CallToolRequest.Extra. go-sdk sets TokenInfo only through
// auth.RequireBearerToken, so this runs it with a verifier that returns the
// existing TokenInfo (if any) plus the signer, presenting a placeholder
// bearer value only for that step and restoring the original Authorization
// header before next runs. The UserID and scopes of an existing TokenInfo
// are preserved; none are invented.
func serveWithToolRequestSigner(w http.ResponseWriter, r *http.Request, next http.Handler, signer *VerifiedSigner) {
	ti := &auth.TokenInfo{}
	if existing := auth.TokenInfoFromContext(r.Context()); existing != nil {
		c := *existing
		ti = &c
		ti.Extra = maps.Clone(existing.Extra)
	}
	if ti.Expiration.IsZero() {
		ti.Expiration = time.Now().Add(time.Duration(wbaMaxWindowSeconds+skewSeconds) * time.Second)
	}
	if ti.Extra == nil {
		ti.Extra = map[string]any{}
	}
	ti.Extra[TokenInfoSignerKey] = signer
	original, hadAuthorization := r.Header["Authorization"]
	inner := http.HandlerFunc(func(w http.ResponseWriter, r2 *http.Request) {
		r2.Header = r2.Header.Clone()
		if hadAuthorization {
			r2.Header["Authorization"] = original
		} else {
			r2.Header.Del("Authorization")
		}
		next.ServeHTTP(w, r2)
	})
	staged := r.Clone(r.Context())
	staged.Body = r.Body
	staged.Header.Set("Authorization", "Bearer adcp-verified-signer")
	auth.RequireBearerToken(func(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
		return ti, nil
	}, nil)(inner).ServeHTTP(w, staged)
}
