package signing

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SignerOptions configures a Signer.
type SignerOptions struct {
	// KeyID MUST match a `kid` in the agent's published JWKS. The signer
	// emits this as the `keyid` sig-param; the verifier uses it to select
	// the public key for verification. Ignored when Provider is set — the
	// provider's KeyID() is used instead.
	KeyID string

	// PrivateKey is the private key half: ed25519.PrivateKey or
	// *ecdsa.PrivateKey on P-256. Use LoadPrivateKey to parse PEM files.
	// Ignored when Provider is set.
	PrivateKey any

	// Provider, when set, supplies the signing operation instead of KeyID +
	// PrivateKey — this is how a Signer signs against a KMS/HSM/Vault-backed
	// key without ever holding the private key material in process memory.
	// KeyID and PrivateKey are ignored when Provider is non-nil; the
	// provider's KeyID() and Algorithm() are used instead.
	//
	// When Provider is nil (the default), NewSigner builds an
	// InMemorySigningProvider from KeyID + PrivateKey internally — this is
	// unchanged from the package's original behavior and remains the
	// default used by tests and by every existing caller of NewSigner.
	//
	// See adcp/v3/signing/awskms for a worked AWS KMS SigningProvider.
	Provider SigningProvider

	// Profile selects the signing profile — tag and required JWK adcp_use.
	// Zero value is ProfileRequestSigning. Outbound webhooks MUST use
	// ProfileWebhookSigning (adcontextprotocol/adcp#2423).
	Profile Profile

	// Clock is the signer's time source. Defaults to time.Now.
	Clock func() time.Time

	// ValidityWindow is how long the signature is valid. Defaults to 300s
	// (the profile maximum). MUST NOT exceed 300s.
	ValidityWindow time.Duration

	// NonceReader supplies randomness for nonces. Defaults to crypto/rand.Reader.
	NonceReader io.Reader
}

// Signer attaches RFC 9421 signatures to http.Requests per the AdCP profile.
type Signer struct {
	opts     SignerOptions
	provider SigningProvider
	alg      Algorithm
}

// NewSigner constructs a signer. Callers supply key material one of two
// ways:
//
//   - SignerOptions.KeyID + PrivateKey (unchanged from prior versions of
//     this package): the key stays in process memory, wrapped internally in
//     an InMemorySigningProvider.
//   - SignerOptions.Provider: any SigningProvider, e.g. a KMS-backed one.
//
// Returns an error if neither path yields a usable provider, if the
// resulting algorithm isn't in the AdCP allowlist, or if required options
// are missing.
func NewSigner(opts SignerOptions) (*Signer, error) {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.ValidityWindow == 0 {
		opts.ValidityWindow = 300 * time.Second
	}
	if opts.ValidityWindow > time.Duration(maxWindowSeconds)*time.Second {
		return nil, fmt.Errorf("signing: ValidityWindow %s exceeds profile max 300s", opts.ValidityWindow)
	}
	if opts.NonceReader == nil {
		opts.NonceReader = rand.Reader
	}
	if opts.Profile.Tag == "" {
		opts.Profile = ProfileRequestSigning
	}

	provider := opts.Provider
	if provider == nil {
		if opts.KeyID == "" {
			return nil, fmt.Errorf("signing: KeyID is required")
		}
		if opts.PrivateKey == nil {
			return nil, fmt.Errorf("signing: PrivateKey is required")
		}
		p, err := NewInMemorySigningProvider(opts.KeyID, opts.PrivateKey)
		if err != nil {
			return nil, err
		}
		provider = p
	}
	if provider.KeyID() == "" {
		return nil, fmt.Errorf("signing: SigningProvider.KeyID() must be non-empty")
	}
	if !provider.Algorithm().Allowed() {
		return nil, fmt.Errorf("signing: unsupported algorithm %q", provider.Algorithm())
	}

	return &Signer{opts: opts, provider: provider, alg: provider.Algorithm()}, nil
}

// Algorithm returns the RFC 9421 alg value produced by this signer.
func (s *Signer) Algorithm() Algorithm { return s.alg }

// SignOptions controls a single SignRequest call.
type SignOptions struct {
	// CoverContentDigest adds "content-digest" to the covered components and
	// attaches a SHA-256 Content-Digest header. Use this when the verifier's
	// covers_content_digest policy is "required" or when the signer wants to
	// commit to the body bytes.
	CoverContentDigest bool

	// CreatedOverride is for testing — forces a specific created timestamp.
	// Implementations should leave this zero.
	CreatedOverride int64

	// NonceOverride is for testing — forces a specific nonce string.
	// Implementations should leave this empty.
	NonceOverride string
}

// SignRequest attaches Signature-Input, Signature, and (optionally)
// Content-Digest headers to r. The body, if present, is fully buffered into
// memory so the digest can be computed and the body can be re-served to
// subsequent RoundTrippers.
//
// The signing operation itself runs against r.Context() — when the
// configured SigningProvider calls out to a KMS/HSM/Vault, that context's
// deadline and cancellation apply to the outbound signing call the same way
// they'd apply to any other network operation triggered by this request.
func (s *Signer) SignRequest(r *http.Request, opts SignOptions) error {
	if r == nil {
		return fmt.Errorf("signing: nil request")
	}

	// Buffer body for digest.
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("signing: read body: %w", err)
		}
		_ = r.Body.Close()
		body = b
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}

	if opts.CoverContentDigest {
		r.Header.Set(contentDigestHeader, computeSHA256DigestHeaderForEncoding(body, s.opts.Profile.contentDigestEncoding()))
	}

	// Construct covered components list.
	covered := []string{componentMethod, componentTargetURI, componentAuthority}
	if ct := r.Header.Get(contentTypeHeader); ct != "" || len(body) > 0 {
		covered = append(covered, componentContentType)
	}
	if opts.CoverContentDigest {
		covered = append(covered, componentContentDigst)
	}

	// Canonicalize @target-uri from the request URL.
	targetURI := r.URL.String()
	if r.URL.Scheme == "" {
		return fmt.Errorf("signing: request URL must be absolute")
	}
	canonicalURI, err := canonicalTargetURI(targetURI)
	if err != nil {
		return fmt.Errorf("signing: canonicalize url: %w", err)
	}
	authority := canonicalAuthority(authorityOf(r), r.URL.Scheme)

	values := map[string]string{
		componentMethod:      strings.ToUpper(r.Method),
		componentTargetURI:   canonicalURI,
		componentAuthority:   authority,
		componentContentType: r.Header.Get(contentTypeHeader),
	}
	if opts.CoverContentDigest {
		values[componentContentDigst] = r.Header.Get(contentDigestHeader)
	}

	// Build sig-params.
	now := s.opts.Clock().Unix()
	created := now
	if opts.CreatedOverride != 0 {
		created = opts.CreatedOverride
	}
	expires := created + int64(s.opts.ValidityWindow/time.Second)
	nonce := opts.NonceOverride
	if nonce == "" {
		nonce, err = generateNonce(s.opts.NonceReader)
		if err != nil {
			return fmt.Errorf("signing: generate nonce: %w", err)
		}
	}

	sigParamsValue := formatSigParams(covered, created, expires, nonce, s.provider.KeyID(), s.alg, s.opts.Profile.Tag)

	base, err := buildSignatureBase(covered, values, sigParamsValue)
	if err != nil {
		return fmt.Errorf("signing: build base: %w", err)
	}

	// Sign. Runs against the request's context so a KMS/HSM-backed
	// SigningProvider's outbound call inherits the caller's deadline and
	// cancellation.
	sigBytes, err := s.provider.Sign(r.Context(), []byte(base))
	if err != nil {
		return fmt.Errorf("signing: sign: %w", err)
	}

	r.Header.Set(signatureInputHeader, "sig1="+sigParamsValue)
	r.Header.Set(signatureHeader, "sig1=:"+encodeBinary(sigBytes, s.opts.Profile.BinaryEncoding)+":")
	return nil
}

// authorityOf returns the authority string (Host header). For client
// requests, http.Request.Host is the preferred source and falls back to URL.Host.
func authorityOf(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

// encodeP1363 left-pads r and s to fieldSize bytes and concatenates.
func encodeP1363(r, s *big.Int, fieldSize int) []byte {
	out := make([]byte, 2*fieldSize)
	rb := r.Bytes()
	sb := s.Bytes()
	copy(out[fieldSize-len(rb):fieldSize], rb)
	copy(out[2*fieldSize-len(sb):], sb)
	return out
}

// formatSigParams serializes the RFC 9421 @signature-params value.
func formatSigParams(covered []string, created, expires int64, nonce, keyid string, alg Algorithm, tag string) string {
	var b strings.Builder
	b.WriteByte('(')
	for i, c := range covered {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		b.WriteString(c)
		b.WriteByte('"')
	}
	b.WriteByte(')')
	b.WriteString(";created=")
	b.WriteString(strconv.FormatInt(created, 10))
	b.WriteString(";expires=")
	b.WriteString(strconv.FormatInt(expires, 10))
	b.WriteString(";nonce=\"")
	b.WriteString(nonce)
	b.WriteString("\";keyid=\"")
	b.WriteString(keyid)
	b.WriteString("\";alg=\"")
	b.WriteString(string(alg))
	b.WriteString("\";tag=\"")
	b.WriteString(tag)
	b.WriteByte('"')
	return b.String()
}

// generateNonce returns a 128-bit base64url-unpadded nonce.
func generateNonce(r io.Reader) (string, error) {
	buf := make([]byte, minNonceBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return b64UrlEncodeRaw(buf), nil
}

// RoundTripper returns an http.RoundTripper that signs every outbound request
// using s before delegating to inner. If inner is nil, http.DefaultTransport
// is used.
//
// coverContentDigest controls whether the signature covers the body bytes.
//
// Signed requests MUST NOT follow HTTP redirects — the @target-uri component
// is bound to the original URL. Callers using this RoundTripper SHOULD set
// http.Client.CheckRedirect to return http.ErrUseLastResponse.
func (s *Signer) RoundTripper(inner http.RoundTripper, coverContentDigest bool) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &signingTransport{signer: s, inner: inner, coverDigest: coverContentDigest}
}

type signingTransport struct {
	signer      *Signer
	inner       http.RoundTripper
	coverDigest bool
}

func (t *signingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Clone to avoid mutating caller's request.
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
	if err := t.signer.SignRequest(cloned, SignOptions{CoverContentDigest: t.coverDigest}); err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(cloned)
}
