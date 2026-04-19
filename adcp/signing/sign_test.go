package signing

import (
	"bytes"
	"crypto/ed25519"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deterministicClock returns a clock that always returns t.
func deterministicClock(t int64) func() time.Time {
	return func() time.Time { return time.Unix(t, 0).UTC() }
}

// fixedReader yields a fixed byte sequence forever.
type fixedReader struct{ buf []byte }

func (f *fixedReader) Read(p []byte) (int, error) {
	if len(f.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.buf)
	return n, nil
}

func loadSpecKeys(t *testing.T) *JWKS {
	t.Helper()
	data, err := os.ReadFile("testdata/request-signing/keys.json")
	require.NoError(t, err)
	jwks, err := ParseJWKS(data)
	require.NoError(t, err)
	return jwks
}

// decodeNonceForFixedReader: the fixed reader must produce the exact nonce
// bytes we need. Spec vector uses nonce="KXYnfEfJ0PBRZXQyVXfVQA" (base64url
// unpadded, 16 bytes). Decode to seed the reader.
func TestSignerReproducesPositive001Signature(t *testing.T) {
	jwks := loadSpecKeys(t)
	ed := jwks.Find("test-ed25519-2026")
	require.NotNil(t, ed)
	priv, err := ed.PrivateKey()
	require.NoError(t, err)

	nonceBytes, err := b64UrlDecode("KXYnfEfJ0PBRZXQyVXfVQA")
	require.NoError(t, err)

	signer, err := NewSigner(SignerOptions{
		KeyID:          ed.Kid,
		PrivateKey:     priv.(ed25519.PrivateKey),
		Clock:          deterministicClock(1776520800),
		ValidityWindow: 300 * time.Second,
		NonceReader:    &fixedReader{buf: nonceBytes},
	})
	require.NoError(t, err)

	body := `{"plan_id":"plan_001","packages":[{"package_id":"pkg_1","budget":{"amount":1000,"currency":"USD"}}]}`
	req, err := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	err = signer.SignRequest(req, SignOptions{CoverContentDigest: false})
	require.NoError(t, err)

	// Check Signature-Input.
	expectedInput := `sig1=("@method" "@target-uri" "@authority" "content-type");created=1776520800;expires=1776521100;nonce="KXYnfEfJ0PBRZXQyVXfVQA";keyid="test-ed25519-2026";alg="ed25519";tag="adcp/request-signing/v1"`
	assert.Equal(t, expectedInput, req.Header.Get("Signature-Input"))

	// Ed25519 is deterministic — signature must match positive/001.
	expectedSig := "sig1=:U51PJzU9nMJxMAH_u-UDpSecT5SQX1-deSnWE3XpFo-BLT2_2h5FgMltntNCW05chhmFnjZEzkRmaYKeU0UUBw:"
	assert.Equal(t, expectedSig, req.Header.Get("Signature"))
}

func TestSignerReproducesPositive002Signature(t *testing.T) {
	jwks := loadSpecKeys(t)
	ed := jwks.Find("test-ed25519-2026")
	require.NotNil(t, ed)
	priv, err := ed.PrivateKey()
	require.NoError(t, err)

	nonceBytes, err := b64UrlDecode("KXYnfEfJ0PBRZXQyVXfVQA")
	require.NoError(t, err)

	signer, err := NewSigner(SignerOptions{
		KeyID:          ed.Kid,
		PrivateKey:     priv.(ed25519.PrivateKey),
		Clock:          deterministicClock(1776520800),
		ValidityWindow: 300 * time.Second,
		NonceReader:    &fixedReader{buf: nonceBytes},
	})
	require.NoError(t, err)

	body := `{"plan_id":"plan_001"}`
	req, err := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	err = signer.SignRequest(req, SignOptions{CoverContentDigest: true})
	require.NoError(t, err)

	assert.Equal(t, "sha-256=:SNIVma8dgUBx/U1CBaYFQnsJep9S0/tXaNXlQQOdoxQ=:", req.Header.Get("Content-Digest"))
	expectedSig := "sig1=:RiD5mPhxpBWhmaqUL5-vceyPX5jpjzYZhSnteuYCIYhIqdIl0Yxdh5qstCPXwkKL4AZOsPBL7-8ctbPkHunSAw:"
	assert.Equal(t, expectedSig, req.Header.Get("Signature"))
}
