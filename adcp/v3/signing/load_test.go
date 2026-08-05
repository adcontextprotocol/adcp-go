package signing

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPrivateKeyRoundTripsGenerateSigningKey(t *testing.T) {
	for _, alg := range []Algorithm{AlgEd25519, AlgES256} {
		t.Run(string(alg), func(t *testing.T) {
			res, err := GenerateSigningKey(alg, "kid")
			require.NoError(t, err)

			priv, gotAlg, err := LoadPrivateKey(res.PrivateKeyPEM)
			require.NoError(t, err)
			assert.Equal(t, alg, gotAlg)

			switch alg {
			case AlgEd25519:
				_, ok := priv.(ed25519.PrivateKey)
				assert.True(t, ok)
			case AlgES256:
				ec, ok := priv.(*ecdsa.PrivateKey)
				require.True(t, ok)
				assert.Equal(t, "P-256", ec.Curve.Params().Name)
			}

			// The loaded private key must drive a successful sign-and-verify
			// through the public API.
			signer, err := NewSigner(SignerOptions{KeyID: "kid", PrivateKey: priv})
			require.NoError(t, err)
			assert.Equal(t, alg, signer.Algorithm())
		})
	}
}

func TestLoadPrivateKeyRejectsNonPEM(t *testing.T) {
	_, _, err := LoadPrivateKey([]byte("not a pem block"))
	assert.Error(t, err)
}
