package signing

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSpecKeysJSON(t *testing.T) {
	data, err := os.ReadFile("testdata/request-signing/keys.json")
	require.NoError(t, err)
	jwks, err := ParseJWKS(data)
	require.NoError(t, err)
	require.Len(t, jwks.Keys, 4)

	ed := jwks.Find("test-ed25519-2026")
	require.NotNil(t, ed)
	assert.Equal(t, "OKP", ed.Kty)
	assert.Equal(t, "Ed25519", ed.Crv)
	assert.Equal(t, "request-signing", ed.AdcpUse)

	pub, err := ed.PublicKey()
	require.NoError(t, err)
	_, ok := pub.(ed25519.PublicKey)
	assert.True(t, ok, "expected ed25519.PublicKey")

	priv, err := ed.PrivateKey()
	require.NoError(t, err)
	edPriv, ok := priv.(ed25519.PrivateKey)
	require.True(t, ok)
	// Signing with the private key and verifying with the public key should work.
	sig := ed25519.Sign(edPriv, []byte("hello"))
	assert.True(t, ed25519.Verify(pub.(ed25519.PublicKey), []byte("hello"), sig))

	es := jwks.Find("test-es256-2026")
	require.NotNil(t, es)
	pub2, err := es.PublicKey()
	require.NoError(t, err)
	ec, ok := pub2.(*ecdsa.PublicKey)
	assert.True(t, ok)
	ecdhPub, err := ec.ECDH()
	require.NoError(t, err)
	assert.NotEmpty(t, ecdhPub.Bytes())
	assert.Equal(t, "request-signing", es.AdcpUse)

	gov := jwks.Find("test-gov-2026")
	require.NotNil(t, gov)
	assert.Equal(t, "governance-signing", gov.AdcpUse)
}

func TestJWKPublicStripsPrivateComponents(t *testing.T) {
	data, err := os.ReadFile("testdata/request-signing/keys.json")
	require.NoError(t, err)
	jwks, err := ParseJWKS(data)
	require.NoError(t, err)

	ed := jwks.Find("test-ed25519-2026")
	require.NotNil(t, ed)
	require.NotEmpty(t, ed.TestOnlyPrivateD, "precondition: spec vector carries private component")

	pub := ed.Public()
	assert.Empty(t, pub.PrivateD)
	assert.Empty(t, pub.TestOnlyPrivateD)
	assert.Equal(t, ed.Kid, pub.Kid)
	assert.Equal(t, ed.X, pub.X)

	marshaled, err := json.Marshal(pub)
	require.NoError(t, err)
	assert.NotContains(t, string(marshaled), `"d"`)
	assert.NotContains(t, string(marshaled), "_private_d_for_test_only")

	// Public must not mutate the receiver.
	assert.NotEmpty(t, ed.TestOnlyPrivateD, "Public must return a copy, not mutate")

	// Also covers the PrivateD (standard "d") path.
	k := JWK{Kid: "x", Kty: "OKP", Crv: "Ed25519", PrivateD: "AAAA"}
	out, err := json.Marshal(k.Public())
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(out), `"d":`), "standard d member must be stripped")
}
