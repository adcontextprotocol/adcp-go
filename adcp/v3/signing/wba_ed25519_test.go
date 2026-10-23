package signing

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWBAIsEd25519Only: the Web Bot Auth path signs and verifies with
// Ed25519 only. An RSA key cannot sign a request or a directory, and an RSA
// key in a directory is a key the profile cannot use: it is skipped, needs
// no signature, and a signature naming it is not read.
func TestWBAIsEd25519Only(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	origin := "https://buyer.example.com"

	r := httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_principal", nil)
	require.Error(t, SignWebBotAuthRequest(r, rsaKey, WBASignOptions{AgentOrigin: origin, Clock: wbaClock}))
	_, err = NewSigner(SignerOptions{PrivateKey: rsaKey, AgentOrigin: origin, Clock: wbaClock})
	require.Error(t, err)
	_, err = NewSigner(SignerOptions{KeyID: "k", PrivateKey: rsaKey})
	require.Error(t, err, "the RFC 9421 profile admits no RSA keys either")
	_, err = PublicDirectoryKey(rsaKey)
	require.Error(t, err)
	_, err = SignKeyDirectory(origin, []any{newEd25519(t), rsaKey}, wbaTestNow, time.Hour)
	require.Error(t, err)

	rsaJWK, err := jwkFromPublicKey(&rsaKey.PublicKey)
	require.NoError(t, err)
	_, err = wbaKeyAlgorithm(rsaJWK)
	require.Error(t, err)
	rsaKid, err := JWKThumbprint(rsaJWK)
	require.NoError(t, err)

	ed := newEd25519(t)
	edJWK, err := PublicDirectoryKey(ed)
	require.NoError(t, err)
	resp := signDirectoryBody(t, origin, []byte(`{"keys":[`+mustJSON(t, rsaJWK)+`,`+mustJSON(t, edJWK)+`]}`), ed)
	resp.Header.Set("Signature-Input", resp.Header.Get("Signature-Input")+`, key9=("@authority";req "content-digest");created=1;expires=2;keyid="`+rsaKid+`";alg="rsa-pss-sha512";tag="`+DirectoryTag+`"`)
	resp.Header.Set("Signature", resp.Header.Get("Signature")+", key9=:AAAA:")
	keys, err := VerifyDirectoryResponse(origin, resp, wbaTestNow)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Contains(t, keys, edJWK.Kid)

	only := signDirectoryBody(t, origin, []byte(`{"keys":[`+mustJSON(t, rsaJWK)+`]}`))
	_, err = VerifyDirectoryResponse(origin, only, wbaTestNow)
	require.ErrorContains(t, err, "no usable key")
}

// TestWBADirectoryRefusesPrivateMaterial: a directory that publishes any of
// the profile's private members (d, p, q, dp, dq, qi, oth, k) is refused,
// whether the member sits on the Ed25519 key that signs the directory or on
// a key the profile skips (P-256, RSA). Without the private member, the
// same directory verifies.
func TestWBADirectoryRefusesPrivateMaterial(t *testing.T) {
	origin := "https://buyer.example.com"
	ed := newEd25519(t)
	edJWK, err := PublicDirectoryKey(ed)
	require.NoError(t, err)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	p256JWK, err := jwkFromPublicKey(&newP256(t).PublicKey)
	require.NoError(t, err)
	rsaJWK, err := jwkFromPublicKey(&rsaKey.PublicKey)
	require.NoError(t, err)
	entries := map[string]string{"P-256": mustJSON(t, p256JWK), "RSA": mustJSON(t, rsaJWK), "Ed25519": mustJSON(t, edJWK)}

	// directory signs a directory holding all three keys, with member added
	// to the named key.
	directory := func(key, member string) *DirectoryResponse {
		withMember := map[string]string{}
		for name, entry := range entries {
			if name == key {
				entry = strings.Replace(entry, `{`, `{`+member+`,`, 1)
			}
			withMember[name] = entry
		}
		body := `{"keys":[` + withMember["P-256"] + `,` + withMember["RSA"] + `,` + withMember["Ed25519"] + `]}`
		return signDirectoryBody(t, origin, []byte(body), ed)
	}
	keys, err := VerifyDirectoryResponse(origin, directory("", ""), wbaTestNow)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Contains(t, keys, edJWK.Kid)

	cases := map[string][2]string{
		"Ed25519 signing key with d": {"Ed25519", `"d":"AAAA"`},
		"P-256 key with d":           {"P-256", `"d":"AAAA"`},
	}
	for _, name := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
		value := `"AAAA"`
		if name == "oth" {
			value = `[{"r":"AAAA","d":"AAAA","t":"AAAA"}]`
		}
		cases["RSA key with "+name] = [2]string{"RSA", `"` + name + `":` + value}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := VerifyDirectoryResponse(origin, directory(tc[0], tc[1]), wbaTestNow)
			require.ErrorContains(t, err, "private key material")
		})
	}
}
