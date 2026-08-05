package signing

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	// the public key for verification.
	KeyID string

	// PrivateKey is the private key half: ed25519.PrivateKey or
	// *ecdsa.PrivateKey on P-256. Use LoadPrivateKey to parse PEM files.
	PrivateKey any

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
	opts SignerOptions
	alg  Algorithm
}

// NewSigner constructs a signer. Returns an error if the private key type
// isn't supported or required options are missing.
func NewSigner(opts SignerOptions) (*Signer, error) {
	if opts.KeyID == "" {
		return nil, fmt.Errorf("signing: KeyID is required")
	}
	if opts.PrivateKey == nil {
		return nil, fmt.Errorf("signing: PrivateKey is required")
	}
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
	var alg Algorithm
	switch opts.PrivateKey.(type) {
	case ed25519.PrivateKey:
		alg = AlgEd25519
	case *ecdsa.PrivateKey:
		ec := opts.PrivateKey.(*ecdsa.PrivateKey)
		if ec.Curve.Params().Name != "P-256" {
			return nil, fmt.Errorf("signing: unsupported ECDSA curve %q (only P-256)", ec.Curve.Params().Name)
		}
		alg = AlgES256
	default:
		return nil, fmt.Errorf("signing: unsupported private key type %T", opts.PrivateKey)
	}
	return &Signer{opts: opts, alg: alg}, nil
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
		r.Header.Set(contentDigestHeader, computeSHA256DigestHeader(body))
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

	sigParamsValue := formatSigParams(covered, created, expires, nonce, s.opts.KeyID, s.alg, s.opts.Profile.Tag)

	base, err := buildSignatureBase(covered, values, sigParamsValue)
	if err != nil {
		return fmt.Errorf("signing: build base: %w", err)
	}

	// Sign.
	sigBytes, err := s.sign([]byte(base))
	if err != nil {
		return fmt.Errorf("signing: sign: %w", err)
	}

	r.Header.Set(signatureInputHeader, "sig1="+sigParamsValue)
	r.Header.Set(signatureHeader, "sig1=:"+base64.RawURLEncoding.EncodeToString(sigBytes)+":")
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

func (s *Signer) sign(base []byte) ([]byte, error) {
	switch key := s.opts.PrivateKey.(type) {
	case ed25519.PrivateKey:
		return ed25519.Sign(key, base), nil
	case *ecdsa.PrivateKey:
		h := sha256.Sum256(base)
		rInt, sInt, err := ecdsa.Sign(rand.Reader, key, h[:])
		if err != nil {
			return nil, err
		}
		// IEEE P1363 (r||s) fixed-width encoding — NOT DER.
		return encodeP1363(rInt, sInt, 32), nil
	default:
		return nil, fmt.Errorf("unsupported key type")
	}
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
