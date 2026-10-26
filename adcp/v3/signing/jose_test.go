package signing

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rfc7638Reference is the RFC 7638 §3 thumbprint computed from the JWK
// member strings, the implementation go-jose replaced. For keys whose
// members are already in RFC 7518 form both must agree.
func rfc7638Reference(t *testing.T, jwk JWK) string {
	t.Helper()
	var members any
	switch jwk.Kty {
	case "OKP":
		members = struct {
			Crv string `json:"crv"`
			Kty string `json:"kty"`
			X   string `json:"x"`
		}{jwk.Crv, jwk.Kty, jwk.X}
	case "EC":
		members = struct {
			Crv string `json:"crv"`
			Kty string `json:"kty"`
			X   string `json:"x"`
			Y   string `json:"y"`
		}{jwk.Crv, jwk.Kty, jwk.X, jwk.Y}
	case "RSA":
		members = struct {
			E   string `json:"e"`
			Kty string `json:"kty"`
			N   string `json:"n"`
		}{jwk.E, jwk.Kty, jwk.N}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(members))
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return b64UrlEncodeRaw(sum[:])
}

func TestJWKThumbprintRFC7638Example(t *testing.T) {
	// RFC 7638 §3.1.
	jwk := JWK{
		Kty: "RSA",
		N:   "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw",
		E:   "AQAB",
	}
	tp, err := JWKThumbprint(jwk)
	require.NoError(t, err)
	require.Equal(t, "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs", tp)
}

// TestJWKThumbprintMatchesMemberStrings pins the go-jose thumbprint to the
// member-string computation for Ed25519, P-256 and RSA keys, including an RSA modulus beyond go-jose's 4096-bit decoder cap.
func TestJWKThumbprintMatchesMemberStrings(t *testing.T) {
	var jwks []JWK
	for range 8 {
		jwk, err := PublicDirectoryKey(newEd25519(t))
		require.NoError(t, err)
		jwks = append(jwks, jwk)
		// P-256 is no Web Bot Auth key type, but JWKThumbprint still
		// computes its thumbprint.
		ec, err := jwkFromPublicKey(&newP256(t).PublicKey)
		require.NoError(t, err)
		jwks = append(jwks, ec)
	}
	for _, bits := range []int{2048, 4096, 8192} {
		// An odd modulus of exactly bits bits: the thumbprint needs no
		// factorization, and generating an 8192-bit key is slow.
		buf := make([]byte, bits/8)
		_, err := rand.Read(buf)
		require.NoError(t, err)
		buf[0] |= 0x80
		buf[len(buf)-1] |= 1
		n := new(big.Int).SetBytes(buf)
		jwks = append(jwks, JWK{Kty: "RSA", N: b64UrlEncodeRaw(n.Bytes()), E: "AQAB"})
	}
	for _, bad := range []JWK{
		{Kty: "RSA", N: "AQAB", E: "AA"}, // e = 0
		{Kty: "RSA", N: "AA", E: "AQAB"}, // n = 0
		{Kty: "OKP", Crv: "X25519", X: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"},
	} {
		_, err := JWKThumbprint(bad)
		require.Error(t, err, "%+v", bad)
	}
	for _, jwk := range jwks {
		got, err := JWKThumbprint(jwk)
		require.NoError(t, err, jwk.Kty)
		require.Equal(t, rfc7638Reference(t, jwk), got, jwk.Kty)
	}
}

// TestJWKPublicKeyAcceptsPaddedMembers keeps the "=" padding b64UrlDecode
// has always accepted on JWK members working through go-jose.
func TestJWKPublicKeyAcceptsPaddedMembers(t *testing.T) {
	jwk, err := PublicDirectoryKey(newEd25519(t))
	require.NoError(t, err)
	want, err := jwk.PublicKey()
	require.NoError(t, err)
	padded := jwk
	padded.X += "="
	got, err := padded.PublicKey()
	require.NoError(t, err)
	require.Equal(t, want, got)

	ec, err := jwkFromPublicKey(&newP256(t).PublicKey)
	require.NoError(t, err)
	ec.X, ec.Y = ec.X+"=", ec.Y+"="
	_, err = ec.PublicKey()
	require.NoError(t, err)

	for _, bad := range []JWK{
		{Kty: "OKP", Crv: "Ed25519", X: "AA"},            // short
		{Kty: "OKP", Crv: "X25519", X: jwk.X},            // not a profile curve
		{Kty: "EC", Crv: "P-384", X: ec.X, Y: ec.Y},      // not a profile curve
		{Kty: "EC", Crv: "P-256", X: ec.X, Y: ec.X},      // not on the curve
		{Kty: "RSA", N: "AQAB", E: "AQAB"},               // not a profile key type
		{Kty: "OKP", Crv: "Ed25519", X: jwk.X + "=="},    // bad padding
		{Kty: "OKP", Crv: "Ed25519", X: jwk.X + "====="}, // over-padded
		{Kty: "OKP", Crv: "Ed25519", X: "AAAA===="},      // over-padded
		{Kty: "OKP", Crv: "Ed25519", X: "a" + jwk.X[1:] + "=b"},
	} {
		_, err := bad.PublicKey()
		require.Error(t, err, "%+v", bad)
	}
}

// TestVerifyRevocationJWSProfileChecks covers the AdCP checks in front of
// go-jose's JWS parsing.
func TestVerifyRevocationJWSProfileChecks(t *testing.T) {
	key := newEd25519(t)
	jwk, err := PublicDirectoryKey(key)
	require.NoError(t, err)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaJWK, err := jwkFromPublicKey(&rsaKey.PublicKey)
	require.NoError(t, err)
	rsaJWK.Kid, err = JWKThumbprint(rsaJWK)
	require.NoError(t, err)
	resolve := func(kid string) (*JWK, error) {
		for _, k := range []JWK{jwk, rsaJWK} {
			if k.Kid == kid {
				return &k, nil
			}
		}
		return nil, nil
	}
	payload := []byte(`{"version":1}`)
	compact := func(h map[string]any) []byte {
		p, pl, s := signJWS(t, key, h, payload)
		return []byte(p + "." + pl + "." + s)
	}

	// An empty crit list asks for no extension.
	got, err := verifyRevocationJWS(compact(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "crit": []string{}}), resolve)
	require.NoError(t, err)
	require.Equal(t, payload, got)

	// "b64": true is the RFC 7515 default and changes nothing.
	got, err = verifyRevocationJWS(compact(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "b64": true}), resolve)
	require.NoError(t, err)
	require.Equal(t, payload, got)

	// alg only in the unprotected header.
	prot, pay, sig := signJWS(t, key, map[string]any{"typ": RevocationListTyp, "kid": jwk.Kid}, payload)
	unprotectedAlg, _ := json.Marshal(map[string]any{"payload": pay, "protected": prot, "signature": sig, "header": map[string]string{"alg": "EdDSA"}})

	// A JSON JWS whose members repeat under case folding.
	prot, pay, sig = signJWS(t, key, map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid}, payload)
	foldedDup := []byte(`{"payload":"` + pay + `","protected":"` + prot + `","signature":"` + sig + `","Signature":"` + sig + `"}`)

	// PS512, with the RFC 7518 §3.5 salt length and with another one: the
	// Web Bot Auth path, revocation lists included, is Ed25519 only.
	pssJWS := func(salt int) []byte {
		hb, err := json.Marshal(map[string]any{"alg": "PS512", "typ": RevocationListTyp, "kid": rsaJWK.Kid})
		require.NoError(t, err)
		input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(payload)
		h := sha512.Sum512([]byte(input))
		s, err := rsa.SignPSS(rand.Reader, rsaKey, crypto.SHA512, h[:], &rsa.PSSOptions{SaltLength: salt, Hash: crypto.SHA512})
		require.NoError(t, err)
		return []byte(input + "." + base64.RawURLEncoding.EncodeToString(s))
	}
	for name, body := range map[string][]byte{
		"protected header repeats kid under case folding": compact(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "KID": "other"}),
		"protected header repeats typ under case folding": compact(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "TYP": "JWT"}),
		"b64 false without crit":                          compact(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "b64": false}),
		"crit not a list":                                 compact(map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "crit": "b64"}),
		"alg only unprotected":                            unprotectedAlg,
		"JSON members fold-dup":                           foldedDup,
		"PS512":                                           pssJWS(64),
		"PS512 with 32-byte salt":                         pssJWS(32),
	} {
		_, err := verifyRevocationJWS(body, resolve)
		require.Error(t, err, name)
	}
}

// TestVerifyRevocationJWSEnvelope pins the envelope rules that run before
// go-jose: the old parser's acceptance is kept, and go-jose's leniency
// (whitespace stripping, non-canonical base64url) does not leak through.
func TestVerifyRevocationJWSEnvelope(t *testing.T) {
	key := newEd25519(t)
	jwk, err := PublicDirectoryKey(key)
	require.NoError(t, err)
	resolve := func(kid string) (*JWK, error) {
		if kid == jwk.Kid {
			k := jwk
			return &k, nil
		}
		return nil, nil
	}
	payload := []byte(`{"version":1}`)
	header := map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid}
	prot, pay, sig := signJWS(t, key, header, payload)

	// Accepted before and after: a "signatures": null member beside the
	// flattened ones, envelope member names in another case (encoding/json
	// matches them), and an unprotected header, which nothing reads.
	for name, body := range map[string]string{
		"null signatures":     `{"payload":"` + pay + `","protected":"` + prot + `","signature":"` + sig + `","signatures":null}`,
		"capitalised members": `{"Payload":"` + pay + `","Protected":"` + prot + `","Signature":"` + sig + `"}`,
		"unprotected header":  `{"payload":"` + pay + `","protected":"` + prot + `","signature":"` + sig + `","header":{"crit":["x"],"nonce":7,"alg":"none"}}`,
	} {
		got, err := verifyRevocationJWS([]byte(body), resolve)
		require.NoError(t, err, name)
		require.Equal(t, payload, got, name)
	}

	// A payload segment with non-zero unused low bits, signed as sent.
	nonCanonical := pay[:len(pay)-1] + string(pay[len(pay)-1]+1)
	_, err = base64.RawURLEncoding.DecodeString(nonCanonical)
	require.NoError(t, err, "still decodes")
	ncSig, err := signWithKey(key, []byte(prot+"."+nonCanonical))
	require.NoError(t, err)
	ncSigB64 := base64.RawURLEncoding.EncodeToString(ncSig)

	mkHeader := func(raw string) string {
		h := base64.RawURLEncoding.EncodeToString([]byte(raw))
		s, err := signWithKey(key, []byte(h+"."+pay))
		require.NoError(t, err)
		return h + "." + pay + "." + base64.RawURLEncoding.EncodeToString(s)
	}
	base := `"alg":"EdDSA","typ":"` + RevocationListTyp + `","kid":"` + jwk.Kid + `"`
	for name, body := range map[string]string{
		"newline in compact payload": prot + "." + pay[:4] + "\n" + pay[4:] + "." + sig,
		"tab in compact signature":   prot + "." + pay + "." + sig[:4] + "\t" + sig[4:],
		"nbsp in compact header":     prot[:4] + " " + prot[4:] + "." + pay + "." + sig,
		"padded compact signature":   prot + "." + pay + "." + sig + "==",
		"non-canonical payload":      prot + "." + nonCanonical + "." + ncSigB64,
		"non-canonical JSON payload": `{"payload":"` + nonCanonical + `","protected":"` + prot + `","signature":"` + ncSigB64 + `"}`,
		"empty compact payload":      prot + ".." + sig,
		"header kid and null KID":    mkHeader(`{` + base + `,"KID":null}`),
		"header nested fold-dup":     mkHeader(`{` + base + `,"ext":{"a":1,"A":2}}`),
		"header not an object":       mkHeader(`[1]`),
	} {
		_, err := verifyRevocationJWS([]byte(body), resolve)
		require.Error(t, err, name)
	}
}

// TestVerifyRevocationJWSProtectedHeaderCap refuses an oversized protected
// header before strictjson or go-jose decode it.
func TestVerifyRevocationJWSProtectedHeaderCap(t *testing.T) {
	key := newEd25519(t)
	jwk, err := PublicDirectoryKey(key)
	require.NoError(t, err)
	resolve := func(string) (*JWK, error) { return &jwk, nil }
	payload := []byte(`{"version":1}`)
	pad := strings.Repeat("[0],", 64<<10)
	header := map[string]any{"alg": "EdDSA", "typ": RevocationListTyp, "kid": jwk.Kid, "z": json.RawMessage("[" + pad + "0]")}
	prot, pay, sig := signJWS(t, key, header, payload)
	require.Greater(t, len(prot), revocationMaxProtectedHeader)
	_, err = verifyRevocationJWS([]byte(prot+"."+pay+"."+sig), resolve)
	require.ErrorContains(t, err, "too large")

	// A header just under the cap still verifies.
	header["z"] = strings.Repeat("a", 5<<10)
	prot, pay, sig = signJWS(t, key, header, payload)
	require.LessOrEqual(t, len(prot), revocationMaxProtectedHeader)
	got, err := verifyRevocationJWS([]byte(prot+"."+pay+"."+sig), resolve)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}
