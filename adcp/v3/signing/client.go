package signing

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
)

// CapabilityProvider returns the seller's request_signing capability for the
// outbound request. Returning nil signals "seller doesn't sign / unknown" and
// the preset skips signing for that request — same fall-through semantics as
// the Python `capability_provider` and the TS `getCapability`.
type CapabilityProvider func(*http.Request) *adcp.RequestSigningCapabilities

// SignedHTTPClientOptions configures NewSignedHTTPClient.
type SignedHTTPClientOptions struct {
	// KeyID, PrivateKey, Profile, ValidityWindow, Clock, NonceReader are
	// passed through to NewSigner.
	KeyID          string
	PrivateKey     any
	Profile        Profile
	ValidityWindow time.Duration
	// Clock and NonceReader are typically set only in tests.
	Clock       func() time.Time
	NonceReader interface{ Read([]byte) (int, error) }

	// CoverContentDigest is the always-sign default for content-digest
	// coverage. Used when CapabilityProvider is nil OR the provider returns
	// nil. Set true when targeting a seller that advertises
	// covers_content_digest="required" (or "either" and you prefer the
	// stricter body-bound option).
	CoverContentDigest bool

	// CapabilityProvider, when non-nil, is consulted per-request. Returning
	// a RequestSigningCapabilities lets the preset:
	//   - decide whether to sign (read required_for / warn_for / supported_for
	//     and their protocol_methods_* counterparts; signs if the operation or
	//     JSON-RPC method is in any of those lists, skips otherwise);
	//   - resolve covers_content_digest per-call ("required" → cover,
	//     "forbidden" → don't, "either"/absent → fall back to
	//     CoverContentDigest).
	// Operation name comes from OperationResolver if set, else from the
	// last segment of the request path (`/adcp/<op>` convention).
	// When nil, every request is signed unconditionally with
	// CoverContentDigest as the digest decision — matches the original
	// preset behavior.
	CapabilityProvider CapabilityProvider

	// OperationResolver derives the AdCP operation name from the outbound
	// request — typically the AdCP method being called. Defaults to
	// PathSuffixOperationResolver, which reads the last path segment of
	// `/adcp/<op>`. Only consulted when CapabilityProvider is also set.
	OperationResolver func(*http.Request) string

	// Inner is the transport the signing layer wraps. Defaults to a clone of
	// http.DefaultTransport with MaxResponseHeaderBytes lowered to
	// DefaultMaxResponseHeaderBytes — net/http's own 10 MiB ceiling would
	// let a peer spend megabytes above the response-size cap, which only
	// covers the body. The clone keeps http.DefaultTransport's settings
	// (including HTTP_PROXY handling) but carries its own connection pool,
	// so reuse the returned client per peer rather than building one per
	// call. Pass a custom transport when you need different limits or
	// isolation (per-tenant proxies, custom mTLS, retry middleware,
	// telemetry) — a transport you supply is used exactly as given.
	Inner http.RoundTripper

	// Timeout is set on the returned *http.Client. Zero means no timeout
	// (the http stdlib default). Most production callers should set one —
	// 30s is a reasonable default for AdCP RPC.
	Timeout time.Duration

	// AgentOrigin opts the client into the experimental AdCP 3.3 Web Bot
	// Auth profile (see SignerOptions.AgentOrigin). KeyID may be empty; it
	// is derived from the key. Leave empty for RFC 9421 signing.
	AgentOrigin string

	// ProfilesProvider returns the seller's request_signing.profiles for a
	// request (nil when the seller omits them, which means the RFC 9421
	// profile only). It applies with CapabilityProvider set, to requests the
	// capability says to sign: with AgentOrigin, the seller must list
	// "web-bot-auth" (a nil provider fails too) or RoundTrip returns
	// ErrWebBotAuthNotAdvertised; without it, a non-nil list must include
	// "adcp/request-signing/v1" or RoundTrip returns
	// ErrRequestSigningNotAdvertised. The client never switches profiles on
	// its own.
	ProfilesProvider func(*http.Request) []string

	// MaxResponseBytes caps the cumulative bytes read from any one agent
	// response. Zero selects DefaultMaxResponseBytes (4 MiB); a negative
	// value (UnlimitedResponseBytes) turns the cap off.
	//
	// The cap is enforced while the body streams — an oversized
	// Content-Length is rejected before the body is read, and otherwise the
	// read that crosses the limit fails with a *ResponseTooLargeError. The
	// body is never silently truncated. Override it for a single call with
	// WithResponseLimits.
	MaxResponseBytes int64

	// MaxSSEEventBytes caps a single event in a text/event-stream response.
	// Zero inherits MaxResponseBytes, which also stays in force as the
	// cumulative per-call cap, so a peer cannot evade the limit by
	// answering with an SSE stream. Set this lower than MaxResponseBytes
	// for a long-lived stream of small events.
	MaxSSEEventBytes int64
}

// ErrRequestSigningNotAdvertised is returned by an RFC 9421 signing client
// whose ProfilesProvider reports profiles that exclude
// "adcp/request-signing/v1".
var ErrRequestSigningNotAdvertised = errors.New("signing: seller does not advertise the adcp/request-signing/v1 request-signing profile")

// ErrWebBotAuthNotAdvertised is returned by a Web Bot Auth signing client
// when the seller does not list "web-bot-auth" in request_signing.profiles.
// The client never falls back to the RFC 9421 profile on its own.
var ErrWebBotAuthNotAdvertised = errors.New("signing: seller does not advertise the web-bot-auth request-signing profile")

// PathSuffixOperationResolver derives the AdCP operation name from the last
// segment of the request path (`/adcp/<op>` convention) — the same shape as
// signing.DefaultOperationResolver on the verifier side.
func PathSuffixOperationResolver(r *http.Request) string {
	return DefaultOperationResolver(r)
}

// NewSignedHTTPClient returns an *http.Client preconfigured to sign outbound
// requests. The client has redirect-following disabled — @target-uri is part
// of the signature base, so a 3xx redirect would silently re-target the
// binding and break verification.
//
// Two modes:
//
//   - Always-sign (CapabilityProvider == nil): every outbound request is
//     signed with CoverContentDigest as the digest decision. Use this when
//     the buyer talks to a single seller whose policy you already know.
//
//   - Capability-aware (CapabilityProvider != nil): per-request, the preset
//     consults the seller's RequestSigningCapabilities and signs only when
//     the operation appears in required_for / warn_for / supported_for, or
//     the JSON-RPC method appears in a protocol_methods_* counterpart.
//     covers_content_digest is honored from the capability ("required" →
//     cover, "forbidden" → don't, "either"/absent → fall back to
//     CoverContentDigest). This matches the Python `capability_provider`
//     and the TS `getCapability` shapes.
//
//     client, err := signing.NewSignedHTTPClient(signing.SignedHTTPClientOptions{
//     KeyID:              "my-agent-2026",
//     PrivateKey:         priv,
//     CoverContentDigest: true,                   // fallback when capability is absent
//     CapabilityProvider: func(*http.Request) *adcp.RequestSigningCapabilities { return cachedCaps },
//     Timeout:            30 * time.Second,
//     })
//
// Both modes cap the size of every agent response at
// DefaultMaxResponseBytes (4 MiB) unless MaxResponseBytes says otherwise.
// Agent responses are untrusted input, and an oversized one is rejected with
// a *ResponseTooLargeError while it streams rather than buffered and
// truncated. See ResponseLimitTransport for the enforcement rules and
// WithResponseLimits for the per-call override.
//
// For lower-level integration (you already have an http.Client and just want
// the always-sign signing transport), see (*Signer).RoundTripper — it does
// not cap responses on its own; wrap it in NewResponseLimitTransport to get
// the same protection.
func NewSignedHTTPClient(opts SignedHTTPClientOptions) (*http.Client, error) {
	if opts.KeyID == "" && opts.AgentOrigin == "" {
		return nil, errors.New("signing.NewSignedHTTPClient: KeyID is required")
	}
	if opts.PrivateKey == nil {
		return nil, errors.New("signing.NewSignedHTTPClient: PrivateKey is required")
	}
	signerOpts := SignerOptions{
		KeyID:          opts.KeyID,
		PrivateKey:     opts.PrivateKey,
		Profile:        opts.Profile,
		Clock:          opts.Clock,
		ValidityWindow: opts.ValidityWindow,
		AgentOrigin:    opts.AgentOrigin,
	}
	if opts.NonceReader != nil {
		signerOpts.NonceReader = opts.NonceReader
	}
	signer, err := NewSigner(signerOpts)
	if err != nil {
		return nil, err
	}

	inner := opts.Inner
	if inner == nil {
		inner = newBoundedHeaderTransport()
	}

	var transport http.RoundTripper
	if opts.CapabilityProvider == nil {
		transport = signer.RoundTripper(inner, opts.CoverContentDigest)
	} else {
		opResolver := opts.OperationResolver
		if opResolver == nil {
			opResolver = PathSuffixOperationResolver
		}
		transport = &capabilityAwareSigningTransport{
			signer:               signer,
			inner:                inner,
			capabilityProvider:   opts.CapabilityProvider,
			operationResolver:    opResolver,
			fallbackCoverContent: opts.CoverContentDigest,
			profilesProvider:     opts.ProfilesProvider,
		}
	}

	// Outermost so it counts the bytes the caller actually reads, including
	// anything net/http transparently decompressed.
	transport = NewResponseLimitTransport(transport, ResponseLimits{
		MaxResponseBytes: opts.MaxResponseBytes,
		MaxSSEEventBytes: opts.MaxSSEEventBytes,
	})

	return &http.Client{
		Transport: transport,
		// @target-uri is signed, so a 3xx redirect re-targets the binding
		// and the verifier rejects with request_signature_invalid. Refuse to
		// follow — callers that need to handle redirects can read the 3xx
		// response and re-issue the request explicitly.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: opts.Timeout,
	}, nil
}

// capabilityAwareSigningTransport reads the seller's request_signing
// capability per request and signs only when the operation appears in
// required_for / warn_for / supported_for. covers_content_digest is honored
// from the capability ("required" → cover, "forbidden" → don't,
// "either"/absent → fall back to fallbackCoverContent).
type capabilityAwareSigningTransport struct {
	signer               *Signer
	inner                http.RoundTripper
	capabilityProvider   CapabilityProvider
	operationResolver    func(*http.Request) string
	fallbackCoverContent bool
	profilesProvider     func(*http.Request) []string
}

func (t *capabilityAwareSigningTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Buffer the body once — both the always-sign and skip paths need to
	// preserve the inner request's body bytes for replay/idempotency.
	cloned := r.Clone(r.Context())
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		cloned.Body = io.NopCloser(bytes.NewReader(body))
	}

	capability := t.capabilityProvider(cloned)
	op := t.operationResolver(cloned)
	if !shouldSignByCapability(capability, op) && !shouldSignProtocolMethodByCapability(capability, protocolMethodFromJSONRPC(body)) {
		return t.inner.RoundTrip(cloned)
	}

	if t.signer.SigningProfile() == ProfileTagWebBotAuth {
		var profiles []string
		if t.profilesProvider != nil {
			profiles = t.profilesProvider(cloned)
		}
		if !slices.Contains(profiles, ProfileTagWebBotAuth) {
			return nil, ErrWebBotAuthNotAdvertised
		}
	} else if t.profilesProvider != nil {
		// An RFC 9421 signer refuses a seller whose advertised profiles
		// exclude it. Omitted profiles (nil) mean the RFC 9421 profile only.
		if profiles := t.profilesProvider(cloned); profiles != nil && !slices.Contains(profiles, ProfileTagRequestSigning) {
			return nil, ErrRequestSigningNotAdvertised
		}
	}

	cover := t.fallbackCoverContent
	if capability != nil {
		switch capability.CoversContentDigest {
		case "required":
			cover = true
		case "forbidden":
			cover = false
		}
	}

	if err := t.signer.SignRequest(cloned, SignOptions{CoverContentDigest: cover}); err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(cloned)
}

func shouldSignProtocolMethodByCapability(capability *adcp.RequestSigningCapabilities, method string) bool {
	if capability == nil || !capability.Supported || method == "" {
		return false
	}
	return slices.Contains(capability.ProtocolMethodsRequiredFor, method) ||
		slices.Contains(capability.ProtocolMethodsWarnFor, method) ||
		slices.Contains(capability.ProtocolMethodsSupportedFor, method)
}

// shouldSignByCapability classifies an outbound operation against the
// seller's advertised policy. Mirrors the Python `operation_needs_signing`
// helper but without the warn-vs-supported distinction (Go currently treats
// any presence in any list as "sign"). Returns false when capability is nil,
// supported=false, or the operation is in none of the three lists.
func shouldSignByCapability(capability *adcp.RequestSigningCapabilities, operation string) bool {
	if capability == nil || !capability.Supported || operation == "" {
		return false
	}
	if slices.Contains(capability.RequiredFor, operation) {
		return true
	}
	if slices.Contains(capability.WarnFor, operation) {
		return true
	}
	return slices.Contains(capability.SupportedFor, operation)
}
