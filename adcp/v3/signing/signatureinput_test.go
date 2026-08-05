package signing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSignatureInputBasic(t *testing.T) {
	h := `sig1=("@method" "@target-uri" "@authority" "content-type");created=1776520800;expires=1776521100;nonce="KXYnfEfJ0PBRZXQyVXfVQA";keyid="test-ed25519-2026";alg="ed25519";tag="adcp/request-signing/v1"`
	got, err := parseSignatureInput(h)
	require.NoError(t, err)
	assert.Equal(t, "sig1", got.label)
	assert.Equal(t, []string{"@method", "@target-uri", "@authority", "content-type"}, got.components)
	assert.EqualValues(t, 1776520800, got.created)
	assert.True(t, got.createdSet)
	assert.EqualValues(t, 1776521100, got.expires)
	assert.True(t, got.expiresSet)
	assert.Equal(t, "KXYnfEfJ0PBRZXQyVXfVQA", got.nonce)
	assert.Equal(t, "test-ed25519-2026", got.keyID)
	assert.Equal(t, "ed25519", got.alg)
	assert.Equal(t, "adcp/request-signing/v1", got.tag)

	// paramsText must equal the value that goes to the right of `sig1=` — i.e., the
	// full @signature-params value: "(...)" + ";..."
	wantParams := `("@method" "@target-uri" "@authority" "content-type");created=1776520800;expires=1776521100;nonce="KXYnfEfJ0PBRZXQyVXfVQA";keyid="test-ed25519-2026";alg="ed25519";tag="adcp/request-signing/v1"`
	assert.Equal(t, wantParams, got.paramsText)
}

func TestParseSignatureInputMultipleLabels(t *testing.T) {
	h := `sig1=("@method" "@target-uri" "@authority" "content-type");created=1776520800;expires=1776521100;nonce="KXYnfEfJ0PBRZXQyVXfVQA";keyid="test-ed25519-2026";alg="ed25519";tag="adcp/request-signing/v1", sig2=("@method" "@target-uri");created=1776520800;expires=1776521100;nonce="DIFFERENT-NONCE-FOR-SIG2____";keyid="test-ed25519-2026";alg="ed25519";tag="adcp/request-signing/v1"`
	got, err := parseSignatureInput(h)
	require.NoError(t, err)
	assert.Equal(t, "sig1", got.label)
	assert.Equal(t, []string{"@method", "@target-uri", "@authority", "content-type"}, got.components)
	// Must stop before the "," — paramsText should not include sig2.
	assert.NotContains(t, got.paramsText, "sig2")
	assert.NotContains(t, got.paramsText, "DIFFERENT-NONCE")
}

func TestParseSignatureInputMalformed(t *testing.T) {
	cases := []string{
		"",
		"this-is-not-a-valid-rfc-9421-signature-input",
		"sig1=",                // no paren
		"sig1=(\"@method\"",    // unterminated parens
		`sig1=("@method");foo`, // param with no =
		`sig1=("@method");created=1;expires=2;nonce="a";keyid="b";alg="c";tag="d", sig1=("@method");created=1;expires=2;nonce="e";keyid="b";alg="c";tag="d"`, // duplicate label
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := parseSignatureInput(in)
			e := AsError(err)
			require.NotNil(t, e, "want *Error, got %v", err)
			assert.Equal(t, CodeHeaderMalformed, e.Code)
		})
	}
}

func TestParseSignature(t *testing.T) {
	h := `sig1=:U51PJzU9nMJxMAH_u-UDpSecT5SQX1-deSnWE3XpFo-BLT2_2h5FgMltntNCW05chhmFnjZEzkRmaYKeU0UUBw:`
	v, err := parseSignature(h, "sig1")
	require.NoError(t, err)
	assert.Equal(t, "U51PJzU9nMJxMAH_u-UDpSecT5SQX1-deSnWE3XpFo-BLT2_2h5FgMltntNCW05chhmFnjZEzkRmaYKeU0UUBw", v)

	// Multiple labels — select sig1.
	h2 := `sig1=:U51PJzU9nMJxMAH_u-UDpSecT5SQX1-deSnWE3XpFo-BLT2_2h5FgMltntNCW05chhmFnjZEzkRmaYKeU0UUBw:, sig2=:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:`
	v2, err := parseSignature(h2, "sig1")
	require.NoError(t, err)
	assert.Equal(t, "U51PJzU9nMJxMAH_u-UDpSecT5SQX1-deSnWE3XpFo-BLT2_2h5FgMltntNCW05chhmFnjZEzkRmaYKeU0UUBw", v2)
}
