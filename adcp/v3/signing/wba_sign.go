package signing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// WBASignOptions configures a Web Bot Auth signature. EXPERIMENTAL (AdCP
// 3.3).
type WBASignOptions struct {
	// AgentOrigin is the signer's identity, an HTTPS origin whose key
	// directory publishes the signing key. Required. A URL with a path is
	// reduced to its canonical origin.
	AgentOrigin string

	// Label is the signature label. Defaults to "sig1".
	Label string

	// ValidityWindow is the signature lifetime, at most 300 s. Defaults to
	// 300 s.
	ValidityWindow time.Duration

	// Clock defaults to time.Now.
	Clock func() time.Time

	// CreatedOverride and NonceOverride are for tests and vectors only.
	CreatedOverride int64
	NonceOverride   string

	// NonceReader supplies nonce randomness. Defaults to crypto/rand.
	NonceReader io.Reader
}

// SignWebBotAuthRequest signs r under the opt-in Web Bot Auth profile. It
// sets Signature-Agent to the agent's origin under the label and covers it,
// and sets tag="web-bot-auth", a nonce of 64 random bytes (unpadded
// base64url), the key's RFC 7638 thumbprint as keyid, and alg="ed25519".
// The key is an ed25519.PrivateKey; other keys, RSA and P-256 included, are
// refused. A request with a body
// must carry Content-Type; the signer sets Content-Digest and covers both.
// Signatures use RFC 8941 sf-binary (standard Base64).
//
// A request carries exactly one Web Bot Auth signature, so a request that
// already carries Signature, Signature-Input, or Signature-Agent is refused.
func SignWebBotAuthRequest(r *http.Request, privateKey any, opts WBASignOptions) error {
	origin, err := webBotAuthOrigin(opts.AgentOrigin)
	if err != nil {
		return fmt.Errorf("signing: agent origin: %w", err)
	}
	return signWebBotAuthRequest(r, privateKey, opts, origin)
}

// signWebBotAuthRequest signs r with origin as its Signature-Agent member
// value, used as given. opts.AgentOrigin is not read.
func signWebBotAuthRequest(r *http.Request, privateKey any, opts WBASignOptions, origin string) error {
	if r == nil {
		return errors.New("signing: nil request")
	}
	for _, name := range []string{signatureHeader, signatureInputHeader, signatureAgentHeader} {
		if len(r.Header.Values(name)) > 0 {
			return errors.New("signing: request is already signed; a Web Bot Auth request carries exactly one signature")
		}
	}
	if opts.Label == "" {
		opts.Label = "sig1"
	}
	if !isValidLabel(opts.Label) {
		return fmt.Errorf("signing: invalid signature label %q", opts.Label)
	}
	window := opts.ValidityWindow
	if window == 0 {
		window = time.Duration(wbaMaxWindowSeconds) * time.Second
	}
	if window <= 0 || window > time.Duration(wbaMaxWindowSeconds)*time.Second {
		return errors.New("signing: Web Bot Auth validity window must be in (0, 300s]")
	}
	if r.URL == nil || !r.URL.IsAbs() {
		return errors.New("signing: request URL must be absolute")
	}
	jwk, err := PublicDirectoryKey(privateKey)
	if err != nil {
		return err
	}
	alg, err := wbaKeyAlgorithm(jwk)
	if err != nil {
		return errors.New("signing: unsupported Web Bot Auth key")
	}

	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
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

	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	created := clock().Unix()
	if opts.CreatedOverride != 0 {
		created = opts.CreatedOverride
	}
	nonce := opts.NonceOverride
	if nonce == "" {
		reader := opts.NonceReader
		if reader == nil {
			reader = rand.Reader
		}
		var buf [wbaNonceBytes]byte
		if _, err := io.ReadFull(reader, buf[:]); err != nil {
			return fmt.Errorf("signing: generate nonce: %w", err)
		}
		nonce = b64UrlEncodeRaw(buf[:])
	}
	if !validWBANonce(nonce) {
		return errors.New("signing: a Web Bot Auth nonce is 64 bytes, as unpadded base64url or padded standard base64")
	}

	staged := r.Header.Clone()
	staged.Set(signatureAgentHeader, sfDict{{key: opts.Label, member: sfMember{item: sfItem{value: sfStringItem(origin)}}}}.serialize())
	components := []sfItem{
		{value: sfStringItem(componentMethod)},
		{value: sfStringItem(componentTargetURI)},
		{value: sfStringItem(componentAuthority)},
	}
	if len(body) > 0 {
		if staged.Get(contentTypeHeader) == "" {
			return errors.New("signing: a Web Bot Auth request with a body requires Content-Type")
		}
		staged.Set(contentDigestHeader, computeSHA256DigestHeaderForEncoding(body, BinaryEncodingRFC8941))
		components = append(components, sfItem{value: sfStringItem(componentContentType)}, sfItem{value: sfStringItem(componentContentDigst)})
	}
	components = append(components, sfItem{value: sfStringItem("signature-agent"), params: []sfParam{{key: "key", value: sfStringItem(opts.Label)}}})
	member := sfMember{inner: true, list: components, params: []sfParam{
		{key: "created", value: sfIntItem(created)},
		{key: "expires", value: sfIntItem(created + int64(window/time.Second))},
		{key: "nonce", value: sfStringItem(nonce)},
		{key: "keyid", value: sfStringItem(jwk.Kid)},
		{key: "alg", value: sfStringItem(string(alg))},
		{key: "tag", value: sfStringItem(ProfileTagWebBotAuth)},
	}}
	base, err := wbaSignatureBase(r.Method, sentTargetURI(r), staged, member, false)
	if err != nil {
		return fmt.Errorf("signing: build Web Bot Auth signature base: %w", err)
	}
	sig, err := signWithKey(privateKey, []byte(base))
	if err != nil {
		return err
	}
	r.Header.Set(signatureAgentHeader, staged.Get(signatureAgentHeader))
	if len(body) > 0 {
		r.Header.Set(contentDigestHeader, staged.Get(contentDigestHeader))
	}
	r.Header.Set(signatureInputHeader, sfDict{{key: opts.Label, member: member}}.serialize())
	r.Header.Set(signatureHeader, sfDict{{key: opts.Label, member: sfMember{item: sfItem{value: sfBare{kind: sfBinary, b: sig}}}}}.serialize())
	return nil
}

// signWithKey signs base with a Web Bot Auth key: a raw Ed25519 signature.
func signWithKey(privateKey any, base []byte) ([]byte, error) {
	k, ok := privateKey.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing: unsupported private key type %T", privateKey)
	}
	return ed25519.Sign(k, base), nil
}
