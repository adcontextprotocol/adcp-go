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
	//   - decide whether to sign (read required_for / warn_for /
	//     supported_for; signs if the operation is in any of those lists,
	//     skips otherwise);
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

	// Inner is the transport the signing layer wraps. Defaults to
	// http.DefaultTransport. http.DefaultTransport honors HTTP_PROXY env
	// vars and shares its connection pool process-wide; pass a custom
	// transport when you need isolation (per-tenant proxies, custom mTLS,
	// retry middleware, telemetry).
	Inner http.RoundTripper

	// Timeout is set on the returned *http.Client. Zero means no timeout
	// (the http stdlib default). Most production callers should set one —
	// 30s is a reasonable default for AdCP RPC.
	Timeout time.Duration
}

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
//     the operation appears in required_for / warn_for / supported_for.
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
// For lower-level integration (you already have an http.Client and just want
// the always-sign signing transport), see (*Signer).RoundTripper.
func NewSignedHTTPClient(opts SignedHTTPClientOptions) (*http.Client, error) {
	if opts.KeyID == "" {
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
		inner = http.DefaultTransport
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
		}
	}

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
}

func (t *capabilityAwareSigningTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Buffer the body once — both the always-sign and skip paths need to
	// preserve the inner request's body bytes for replay/idempotency.
	cloned := r.Clone(r.Context())
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		cloned.Body = io.NopCloser(bytes.NewReader(body))
	}

	capability := t.capabilityProvider(cloned)
	op := t.operationResolver(cloned)
	if !shouldSignByCapability(capability, op) {
		return t.inner.RoundTrip(cloned)
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
