package signing

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSigningKeyEd25519(t *testing.T) {
	res, err := GenerateSigningKey(AlgEd25519, "my-kid")
	require.NoError(t, err)
	assert.Equal(t, "my-kid", res.PublicJWK.Kid)
	assert.Equal(t, "OKP", res.PublicJWK.Kty)
	assert.Equal(t, "Ed25519", res.PublicJWK.Crv)
	assert.Equal(t, "request-signing", res.PublicJWK.AdcpUse)
	assert.Equal(t, "sig", res.PublicJWK.Use)
	assert.Contains(t, res.PublicJWK.KeyOps, "verify")

	block, _ := pem.Decode(res.PrivateKeyPEM)
	require.NotNil(t, block)
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	edPriv, ok := priv.(ed25519.PrivateKey)
	require.True(t, ok)

	// Round-trip: sign arbitrary bytes and verify with the public key from the JWK.
	pubIface, err := res.PublicJWK.PublicKey()
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(pubIface.(ed25519.PublicKey), []byte("hello"), ed25519.Sign(edPriv, []byte("hello"))))
}

func TestGenerateSigningKeyES256(t *testing.T) {
	res, err := GenerateSigningKey(AlgES256, "")
	require.NoError(t, err)
	assert.Equal(t, "EC", res.PublicJWK.Kty)
	assert.Equal(t, "P-256", res.PublicJWK.Crv)
	assert.NotEmpty(t, res.PublicJWK.Kid)

	block, _ := pem.Decode(res.PrivateKeyPEM)
	require.NotNil(t, block)
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	_, ok := priv.(*ecdsa.PrivateKey)
	require.True(t, ok)

	_, err = res.PublicJWK.PublicKey()
	require.NoError(t, err)
}
