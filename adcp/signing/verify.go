package signing

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"
)

// VerifyOptions configures a single call to VerifyRequestSignature.
type VerifyOptions struct {
	// OperationName is the AdCP protocol operation the request targets.
	// Used to check RequiredFor at the pre-check step. Verifiers derive this
	// from the request path or MCP/A2A tool name.
	OperationName string

	// RequiredFor lists operations whose unsigned requests must be rejected
	// with request_signature_required.
	RequiredFor []string

	// Profile selects which signing profile this verifier accepts. Zero value
	// is ProfileRequestSigning. Webhook receivers MUST set
	// ProfileWebhookSigning — a request-signing signature presented to a
	// webhook endpoint (or vice versa) is rejected with
	// webhook_signature_tag_invalid.
	Profile Profile

	// ContentDigestPolicy controls whether content-digest coverage is
	// required, forbidden, or optional (DigestEither, the default).
	ContentDigestPolicy DigestPolicy

	// Scheme is the URL scheme of the request (https/http). Go's http.Request
	// does not preserve this on incoming requests; the middleware layer
	// supplies it. Defaults to "https".
	Scheme string

	// Resolver resolves keyid to a JWK + agent URL.
	Resolver JWKSResolver

	// Revocation reports key revocation state.
	Revocation RevocationSource

	// Replay deduplicates (keyid, nonce) pairs.
	Replay ReplayStore

	// Clock supplies the verifier's wall clock. Defaults to time.Now.
	Clock func() time.Time

	// MaxBodyBytes caps the request body size buffered for content-digest
	// recompute (step 11). Requests larger than this are rejected with
	// request_signature_digest_mismatch. 0 selects a 32 MiB default.
	MaxBodyBytes int64
}

// VerifyRequestSignature implements the 13-step AdCP verifier checklist plus
// the pre-check and sub-step 9a.
//
// Returns:
//
//   - (*VerifiedSigner, nil) on a successfully-verified signature.
//   - (nil, nil) when the request is unsigned AND the operation is not in
//     RequiredFor; callers MAY proceed with bearer-only auth.
//   - (nil, *Error) on rejection. The *Error's Code is stable and safe to
//     emit in `WWW-Authenticate: Signature error="<code>"`.
func VerifyRequestSignature(r *http.Request, opts VerifyOptions) (*VerifiedSigner, error) {
	if opts.Resolver == nil {
		return nil, newError(CodeJWKSUnavailable, "resolver not configured")
	}
	if opts.Replay == nil {
		return nil, newError(CodeReplayed, "replay store not configured")
	}
	profile := opts.Profile
	if profile.Tag == "" {
		profile = ProfileRequestSigning
	}
	now := time.Now
	if opts.Clock != nil {
		now = opts.Clock
	}
	scheme := opts.Scheme
	if scheme == "" {
		scheme = "https"
	}
	requiredFor := map[string]struct{}{}
	for _, op := range opts.RequiredFor {
		requiredFor[op] = struct{}{}
	}

	sigInputHeader := r.Header.Get(signatureInputHeader)
	sigHeader := r.Header.Get(signatureHeader)

	// Reject multi-valued Content-Type and Content-Digest headers — the profile
	// covers a single canonical value; two Field instances are an interop hazard.
	if vals := r.Header.Values(contentTypeHeader); len(vals) > 1 {
		return nil, newError(CodeHeaderMalformed, "multiple Content-Type values")
	}
	if vals := r.Header.Values(contentDigestHeader); len(vals) > 1 {
		return nil, newError(CodeHeaderMalformed, "multiple Content-Digest values")
	}

	// Pre-check 0: required_for + header-pair enforcement.
	if sigInputHeader == "" && sigHeader == "" {
		if _, req := requiredFor[opts.OperationName]; req {
			return nil, newError(CodeRequired, "operation requires signature")
		}
		return nil, nil // unsigned, not required; caller proceeds with bearer auth
	}
	if sigInputHeader == "" || sigHeader == "" {
		return nil, newError(CodeHeaderMalformed, "Signature and Signature-Input are a bound pair")
	}

	// Step 1: parse.
	parsed, err := parseSignatureInput(sigInputHeader)
	if err != nil {
		return nil, err
	}

	// Step 2: required params.
	if !parsed.createdSet || !parsed.expiresSet || !parsed.nonceSet ||
		!parsed.keyIDSet || !parsed.algSet || !parsed.tagSet {
		return nil, newError(CodeParamsIncomplete, "required sig-param absent")
	}

	// Step 3: tag.
	if parsed.tag != profile.Tag {
		return nil, newError(CodeTagInvalid, "tag not "+profile.Tag)
	}

	// Step 4: alg allowlist.
	alg := Algorithm(parsed.alg)
	if !alg.Allowed() {
		return nil, newError(CodeAlgNotAllowed, "alg not in AdCP allowlist")
	}

	// Step 5: window.
	nowT := now().Unix()
	if parsed.expires <= parsed.created {
		return nil, newError(CodeWindowInvalid, "expires <= created")
	}
	if parsed.created > nowT+skewSeconds {
		return nil, newError(CodeWindowInvalid, "created too far in future")
	}
	if parsed.expires < nowT-skewSeconds {
		return nil, newError(CodeWindowInvalid, "signature expired")
	}
	if parsed.expires-parsed.created > maxWindowSeconds {
		return nil, newError(CodeWindowInvalid, "validity window exceeds 300s")
	}

	// Nonce length check (≥ 128 bits decoded; base64url unpadded, no =).
	if strings.Contains(parsed.nonce, "=") {
		return nil, newError(CodeParamsIncomplete, "nonce contains padding")
	}
	nBytes, decErr := b64UrlDecode(parsed.nonce)
	if decErr != nil || len(nBytes) < minNonceBytes {
		return nil, newError(CodeParamsIncomplete, "nonce too short or invalid base64url")
	}
	// Dedup on the decoded nonce bytes so future encoding variants collapse
	// to the same replay key.
	dedupNonce := string(nBytes)

	// Step 6: components.
	hasMethod := slices.Contains(parsed.components, componentMethod)
	hasTargetURI := slices.Contains(parsed.components, componentTargetURI)
	hasAuthority := slices.Contains(parsed.components, componentAuthority)
	hasContentType := slices.Contains(parsed.components, componentContentType)
	hasContentDigest := slices.Contains(parsed.components, componentContentDigst)

	if !hasMethod || !hasTargetURI || !hasAuthority {
		return nil, newError(CodeComponentsIncomplete, "missing required covered component")
	}
	hasBody := r.ContentLength > 0 || (r.Body != nil && r.Body != http.NoBody)
	// Heuristic: if Content-Type header is present, there is a body intent.
	// Spec: "Required on requests with bodies."
	if (hasBody || r.Header.Get(contentTypeHeader) != "") && !hasContentType {
		return nil, newError(CodeComponentsIncomplete, "content-type not covered")
	}
	policy := opts.ContentDigestPolicy
	if policy == "" {
		policy = DigestEither
	}
	switch policy {
	case DigestRequired:
		if !hasContentDigest {
			return nil, newError(CodeComponentsIncomplete, "policy requires content-digest coverage")
		}
	case DigestForbidden:
		if hasContentDigest {
			return nil, newError(CodeComponentsUnexpected, "policy forbids content-digest coverage")
		}
	}

	// Step 7: JWK resolution.
	jwk, agentURL, err := opts.Resolver.Resolve(r.Context(), parsed.keyID)
	if err != nil {
		if asErr := AsError(err); asErr != nil {
			return nil, asErr
		}
		return nil, wrapError(CodeKeyUnknown, "resolver error", err)
	}

	// Step 8: key purpose + alg cross-check.
	if jwk.Use != "sig" || !slices.Contains(jwk.KeyOps, "verify") || jwk.AdcpUse != profile.AdcpUse {
		return nil, newError(CodeKeyPurposeInvalid, "key not scoped for "+profile.AdcpUse)
	}
	jwkAlgV, err := jwk.SigParamAlg()
	if err != nil || jwkAlgV != alg {
		return nil, newError(CodeKeyPurposeInvalid, "JWK curve does not match sig-param alg")
	}
	// When the JWK self-declares an alg member, it must be the JWS-level name
	// matching the kty/crv — defense-in-depth against JWKS misconfiguration.
	if jwk.Alg != "" {
		var want jwkAlg
		switch alg {
		case AlgEd25519:
			want = jwkAlgEdDSA
		case AlgES256:
			want = jwkAlgES256
		}
		if jwkAlg(jwk.Alg) != want {
			return nil, newError(CodeKeyPurposeInvalid, "JWK alg member does not match sig-param alg")
		}
	}

	// Step 9: revocation (before crypto verify).
	if opts.Revocation != nil {
		if opts.Revocation.Stale() {
			return nil, newError(CodeRevocationStale, "revocation list stale")
		}
		if opts.Revocation.Revoked(parsed.keyID) {
			return nil, newError(CodeKeyRevoked, "keyid revoked")
		}
	}

	// Step 9a: per-keyid cap (before crypto verify).
	if opts.Replay.HitCap(parsed.keyID) {
		return nil, newError(CodeRateAbuse, "per-keyid replay cache cap exceeded")
	}

	// Step 10: cryptographic verify.
	pub, err := jwk.PublicKey()
	if err != nil {
		return nil, wrapError(CodeKeyPurposeInvalid, "jwk public key invalid", err)
	}

	// Build the canonical signature base from the request.
	canonicalURI, err := canonicalTargetURI(reconstructRequestURL(r, scheme))
	if err != nil {
		return nil, wrapError(CodeHeaderMalformed, "canonicalize url", err)
	}
	authority := canonicalAuthority(authorityOf(r), scheme)

	values := map[string]string{
		componentMethod:      strings.ToUpper(r.Method),
		componentTargetURI:   canonicalURI,
		componentAuthority:   authority,
		componentContentType: r.Header.Get(contentTypeHeader),
	}
	if hasContentDigest {
		values[componentContentDigst] = r.Header.Get(contentDigestHeader)
	}
	base, err := buildSignatureBase(parsed.components, values, parsed.paramsText)
	if err != nil {
		return nil, wrapError(CodeHeaderMalformed, "build signature base", err)
	}

	sigValue, err := parseSignature(sigHeader, parsed.label)
	if err != nil {
		return nil, err
	}
	sigBytes, err := b64UrlDecode(sigValue)
	if err != nil {
		return nil, newError(CodeHeaderMalformed, "Signature value not base64url")
	}

	if !verifySignature(alg, pub, []byte(base), sigBytes) {
		return nil, newError(CodeInvalid, "cryptographic verification failed")
	}

	// Step 11: content-digest recompute.
	if hasContentDigest {
		limit := opts.MaxBodyBytes
		if limit <= 0 {
			limit = defaultMaxBodyBytes
		}
		bodyBytes, err := readAndReplaceBody(r, limit)
		if err != nil {
			return nil, wrapError(CodeDigestMismatch, "read body for digest", err)
		}
		headerVal := r.Header.Get(contentDigestHeader)
		raw, ok, derr := extractSHA256FromDigestHeader(headerVal)
		if derr != nil {
			return nil, derr
		}
		if !ok {
			return nil, newError(CodeDigestMismatch, "sha-256 not in Content-Digest header")
		}
		actual := sha256.Sum256(bodyBytes)
		if !bytes.Equal(raw, actual[:]) {
			return nil, newError(CodeDigestMismatch, "content-digest != recomputed body hash")
		}
	}

	// Step 12: replay check.
	if opts.Replay.Seen(parsed.keyID, dedupNonce) {
		return nil, newError(CodeReplayed, "nonce already seen within window")
	}

	// Step 13: insert into replay cache.
	ttl := time.Duration(parsed.expires-nowT+skewSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Duration(skewSeconds) * time.Second
	}
	if !opts.Replay.Insert(parsed.keyID, dedupNonce, ttl) {
		return nil, newError(CodeRateAbuse, "replay cache insert rejected (cap hit)")
	}

	return &VerifiedSigner{
		KeyID:      parsed.keyID,
		AgentURL:   agentURL,
		VerifiedAt: time.Unix(nowT, 0).UTC(),
		Algorithm:  alg,
		Label:      parsed.label,
	}, nil
}

// reconstructRequestURL returns the absolute URL of r, using scheme as the
// scheme since incoming server requests lack one. Falls back to r.URL.String()
// when r.URL is already absolute (e.g., client-side tests).
func reconstructRequestURL(r *http.Request, scheme string) string {
	if r.URL.IsAbs() {
		return r.URL.String()
	}
	u := *r.URL
	u.Scheme = scheme
	u.Host = r.Host
	return u.String()
}

// readAndReplaceBody buffers up to limit+1 bytes of r.Body so the digest can
// be recomputed, then restores a reader so downstream handlers can read again.
// Returns an error when the body exceeds limit; a memory-DoS guard.
func readAndReplaceBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	lr := io.LimitReader(r.Body, limit+1)
	b, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	if int64(len(b)) > limit {
		return nil, newError(CodeDigestMismatch, "request body exceeds MaxBodyBytes")
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

// verifySignature returns true if sig is a valid signature over base for the
// given public key and AdCP algorithm.
func verifySignature(alg Algorithm, pub any, base, sig []byte) bool {
	switch alg {
	case AlgEd25519:
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok || len(sig) != ed25519.SignatureSize {
			return false
		}
		return ed25519.Verify(edPub, base, sig)
	case AlgES256:
		ecPub, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return false
		}
		// IEEE P1363 (r||s) — two 32-byte halves on P-256.
		if len(sig) != 64 {
			return false
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if r.Sign() <= 0 || s.Sign() <= 0 {
			return false
		}
		h := sha256.Sum256(base)
		return ecdsa.Verify(ecPub, h[:], r, s)
	default:
		return false
	}
}

