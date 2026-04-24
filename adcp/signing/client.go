package signing

import (
	"errors"
	"net/http"
	"time"
)

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

	// CoverContentDigest controls whether the signature base covers
	// content-digest. Set true when the seller's request_signing capability
	// has covers_content_digest="required" (or "either" and you prefer the
	// stricter body-bound option).
	CoverContentDigest bool

	// Inner is the transport the signing layer wraps. Defaults to
	// http.DefaultTransport. Pass a custom transport (mTLS, retries,
	// telemetry) to compose with signing.
	Inner http.RoundTripper

	// Timeout is set on the returned *http.Client. Zero means no timeout
	// (the http stdlib default). Most production callers should set one —
	// 30s is a reasonable default for AdCP RPC.
	Timeout time.Duration
}

// NewSignedHTTPClient returns an *http.Client preconfigured to sign every
// outbound request with the supplied key. The client has redirect-following
// disabled — @target-uri is part of the signature base, so a 3xx redirect
// would silently re-target the binding and break verification.
//
// Use this preset for adapters that talk to a single seller (or want every
// outbound request signed regardless of target). For capability-gated
// signing (sign only operations the seller listed in required_for /
// warn_for / supported_for), wrap with a per-call decision or keep separate
// clients for the signed and unsigned paths.
//
//	client, err := signing.NewSignedHTTPClient(signing.SignedHTTPClientOptions{
//	    KeyID:              "my-agent-2026",
//	    PrivateKey:         priv,
//	    CoverContentDigest: true,                   // seller advertised covers_content_digest=required
//	    Timeout:            30 * time.Second,
//	})
//	if err != nil {
//	    return err
//	}
//	resp, err := client.Post("https://seller.example.com/adcp/create_media_buy", "application/json", body)
//
// For lower-level integration (you already have an http.Client and just want
// the signing transport), see (*Signer).RoundTripper.
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
	transport := signer.RoundTripper(inner, opts.CoverContentDigest)

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
