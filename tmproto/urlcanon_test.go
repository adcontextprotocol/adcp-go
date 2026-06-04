package tmproto

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalizeURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://www.oakwood.example.com/2026/03/kitchen-trends", "oakwood.example.com/2026/03/kitchen-trends"},
		{"http://m.oakwood.example.com/2026/03/kitchen-trends", "oakwood.example.com/2026/03/kitchen-trends"},
		{"https://oakwood.example.com/2026/03/kitchen-trends/", "oakwood.example.com/2026/03/kitchen-trends"},
		{"https://WWW.Oakwood.Example.COM/Path", "oakwood.example.com/path"},
		{"https://amp.oakwood.example.com/article?utm_source=twitter#top", "oakwood.example.com/article"},
		{"oakwood.example.com/page", "oakwood.example.com/page"},
		{"https://example.com", "example.com"},
		{"https://example.com/", "example.com"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := CanonicalizeURL(tt.input)
			assert.Equal(t, tt.expected, got, "CanonicalizeURL(%q)", tt.input)
		})
	}
}

func TestHashURL_SameURLsDifferentForms(t *testing.T) {
	urls := []string{
		"https://www.oakwood.example.com/2026/03/kitchen-trends",
		"http://m.oakwood.example.com/2026/03/kitchen-trends",
		"https://oakwood.example.com/2026/03/kitchen-trends/",
		"https://WWW.OAKWOOD.EXAMPLE.COM/2026/03/kitchen-trends",
	}
	hash0 := HashURL(urls[0])
	for _, u := range urls[1:] {
		assert.Equal(t, hash0, HashURL(u), "HashURL(%q) != HashURL(%q)", u, urls[0])
	}
}

func TestHashURL_DifferentURLs(t *testing.T) {
	h1 := HashURL("https://oakwood.example.com/page-a")
	h2 := HashURL("https://oakwood.example.com/page-b")
	assert.NotEqual(t, h1, h2, "different URLs should produce different hashes")
}

// TestHashURL_WireShape pins the AdCP-spec wire shape for
// ArtifactRefTypeURLHash: standard base64 (RFC 4648 §4) of a 32-byte
// Blake3 digest. 44 characters total including padding. Publishers
// produce values in this exact shape; the context-agent stores them
// in url:blocklist:* / url:allowlist:* set members byte-for-byte.
func TestHashURL_WireShape(t *testing.T) {
	h := HashURL("https://oakwood.example.com/article")
	assert.Len(t, h, 44, "Blake3-256 in standard base64 is exactly 44 chars including padding")

	decoded, err := base64.StdEncoding.DecodeString(h)
	assert.NoError(t, err, "must decode as standard base64")
	assert.Len(t, decoded, 32, "decoded digest must be 32 bytes (Blake3-256)")
}

// TestHashURL_EmptyCanonicalProducesStableHash pins that the empty
// canonical URL still produces a deterministic hash (Blake3 of the
// empty byte slice) rather than a panic or the empty string.
// Defensive: handlers MAY skip empty refs at the validation layer,
// but HashURL itself MUST be total.
func TestHashURL_EmptyCanonicalProducesStableHash(t *testing.T) {
	h := HashURL("")
	assert.Len(t, h, 44)
	assert.Equal(t, h, HashURL(""), "deterministic")
}
