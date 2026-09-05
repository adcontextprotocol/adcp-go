package signing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalTargetURI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"basic", "https://seller.example.com/adcp/create_media_buy", "https://seller.example.com/adcp/create_media_buy"},
		{"strip 443", "https://seller.example.com:443/adcp/create_media_buy", "https://seller.example.com/adcp/create_media_buy"},
		{"strip 80 http", "http://seller.example.com:80/x", "http://seller.example.com/x"},
		{"keep non-default port", "https://seller.example.com:8443/x", "https://seller.example.com:8443/x"},
		{"dot segment collapse", "https://seller.example.com/adcp/./create_media_buy", "https://seller.example.com/adcp/create_media_buy"},
		{"dot-dot", "https://seller.example.com/a/b/../c", "https://seller.example.com/a/c"},
		{"preserve query byte order", "https://seller.example.com/x?b=2&a=1&c=3", "https://seller.example.com/x?b=2&a=1&c=3"},
		{"uppercase percent hex", "https://seller.example.com/adcp/resource/%e2%98%83/item", "https://seller.example.com/adcp/resource/%E2%98%83/item"},
		{"strip userinfo", "https://user:pass@seller.example.com/x", "https://seller.example.com/x"},
		{"strip fragment", "https://seller.example.com/x#frag", "https://seller.example.com/x"},
		{"lowercase scheme and host", "HTTPS://SELLER.EXAMPLE.COM/x", "https://seller.example.com/x"},
		{"empty path to slash", "https://seller.example.com", "https://seller.example.com/"},
		{"decode unreserved %7E", "https://seller.example.com/%7Euser/x", "https://seller.example.com/~user/x"},
		{"decode unreserved %2D %5F %2E", "https://seller.example.com/a%2Db%5Fc%2Ed", "https://seller.example.com/a-b_c.d"},
		{"keep reserved encoded uppercased", "https://seller.example.com/a%2fb", "https://seller.example.com/a%2Fb"},
		{"IPv6 literal", "https://[2001:db8::1]/x", "https://[2001:db8::1]/x"},
		{"IPv6 with non-default port", "https://[2001:db8::1]:8443/x", "https://[2001:db8::1]:8443/x"},
		{"IPv6 with default 443 stripped", "https://[2001:db8::1]:443/x", "https://[2001:db8::1]/x"},
		{"strip DNS root dot", "https://seller.example.com./x", "https://seller.example.com/x"},
		{"strip DNS root dot with port", "https://seller.example.com.:8443/x", "https://seller.example.com:8443/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalTargetURI(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCanonicalAuthority(t *testing.T) {
	assert.Equal(t, "seller.example.com", canonicalAuthority("Seller.Example.COM", "https"))
	assert.Equal(t, "seller.example.com", canonicalAuthority("seller.example.com:443", "https"))
	assert.Equal(t, "seller.example.com", canonicalAuthority("seller.example.com:80", "http"))
	assert.Equal(t, "seller.example.com:8443", canonicalAuthority("seller.example.com:8443", "https"))
	assert.Equal(t, "[2001:db8::1]:8443", canonicalAuthority("[2001:db8::1]:8443", "https"))
	assert.Equal(t, "[2001:db8::1]", canonicalAuthority("[2001:db8::1]:443", "https"))
}

func TestCanonicalTargetURIRejectsNonASCII(t *testing.T) {
	// Raw U-label — signer MUST convert to A-label first.
	_, err := canonicalTargetURI("https://例え.example/x")
	require.Error(t, err)
}

func TestCanonicalTargetURIRejectsMalformedHost(t *testing.T) {
	for _, rawURL := range []string{
		"https://seller.example.com../x",
		"https://seller..example.com/x",
		"https:///x",
	} {
		_, err := canonicalTargetURI(rawURL)
		require.Error(t, err, rawURL)
	}
}

func TestBuildSignatureBaseMatchesPositive001(t *testing.T) {
	// From testdata/request-signing/positive/001-basic-post.json
	want := "\"@method\": POST\n" +
		"\"@target-uri\": https://seller.example.com/adcp/create_media_buy\n" +
		"\"@authority\": seller.example.com\n" +
		"\"content-type\": application/json\n" +
		`"@signature-params": ("@method" "@target-uri" "@authority" "content-type");created=1776520800;expires=1776521100;nonce="KXYnfEfJ0PBRZXQyVXfVQA";keyid="test-ed25519-2026";alg="ed25519";tag="adcp/request-signing/v1"`

	covered := []string{"@method", "@target-uri", "@authority", "content-type"}
	values := map[string]string{
		"@method":      "POST",
		"@target-uri":  "https://seller.example.com/adcp/create_media_buy",
		"@authority":   "seller.example.com",
		"content-type": "application/json",
	}
	sigParams := `("@method" "@target-uri" "@authority" "content-type");created=1776520800;expires=1776521100;nonce="KXYnfEfJ0PBRZXQyVXfVQA";keyid="test-ed25519-2026";alg="ed25519";tag="adcp/request-signing/v1"`

	got, err := buildSignatureBase(covered, values, sigParams)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
