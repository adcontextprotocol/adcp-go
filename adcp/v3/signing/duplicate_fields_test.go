package signing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSignatureFieldsRejectDuplicates: a repeated parameter or dictionary
// label is malformed, never resolved last-wins (or first-wins), because two
// parsers could otherwise disagree about what was signed.
func TestSignatureFieldsRejectDuplicates(t *testing.T) {
	const params = `;created=1;expires=2;nonce="n";keyid="k";alg="ed25519";tag="t"`
	for name, input := range map[string]string{
		"duplicate created":           `sig1=("@method")` + params + `;created=3`,
		"duplicate tag":               `sig1=("@method")` + params + `;tag="other"`,
		"duplicate unknown parameter": `sig1=("@method")` + params + `;x=1;x=2`,
		"duplicate first label":       `sig1=("@method")` + params + `, sig1=("@method")` + params,
		"duplicate later label":       `sig1=("@method")` + params + `, sig2=("@method")` + params + `, sig2=("@authority")` + params,
	} {
		_, err := parseSignatureInput(input)
		require.Error(t, err, name)
		e := AsError(err)
		require.NotNil(t, e, name)
		require.Equal(t, CodeHeaderMalformed, e.Code, name)
	}
	_, err := parseSignatureInput(`sig1=("@method")` + params + `, sig2=("@method")` + params)
	require.NoError(t, err, "distinct labels are fine")

	for name, header := range map[string]string{
		"duplicate chosen label": `sig1=:AAAA:, sig1=:BBBB:`,
		"duplicate other label":  `sig1=:AAAA:, sig2=:BBBB:, sig2=:CCCC:`,
	} {
		_, err := parseSignature(header, "sig1")
		e := AsError(err)
		require.NotNil(t, e, name)
		require.Equal(t, CodeHeaderMalformed, e.Code, name)
	}
	v, err := parseSignature(`sig0=:ZZZZ:, sig1=:AAAA:`, "sig1")
	require.NoError(t, err)
	require.Equal(t, "AAAA", v)
}
