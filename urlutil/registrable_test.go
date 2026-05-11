package urlutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/urlutil"
)

func TestRegistrable(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"https subdomain", "https://adsb.yahoo.com", "yahoo.com"},
		{"schemeless with path", "abc.google.com/test/v1", "google.com"},
		{"http with port", "http://news.bbc.co.uk:8080/world", "bbc.co.uk"},
		{"bare hostname", "example.com", "example.com"},
		{"deep subdomain", "a.b.c.example.co.uk", "example.co.uk"},
		{"userinfo", "https://user:pass@adsb.yahoo.com/path", "yahoo.com"},
		{"mixed case multi-label suffix", "https://WWW.YAHOO.CO.UK/x", "yahoo.co.uk"},
		{"upper case host with port", "HTTPS://NEWS.BBC.CO.UK:8080/world", "bbc.co.uk"},
		{"schemeless with embedded url in query", "example.com/foo?url=https://other.com", "example.com"},
		{"schemeless with colon-slash-slash in path", "abc.google.com/test://weird", "google.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := urlutil.Registrable(tc.input)
			require.NoError(t, err, "Registrable(%q)", tc.input)
			assert.Equal(t, tc.want, got, "Registrable(%q)", tc.input)
		})
	}
}

func TestRegistrable_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"localhost", "http://localhost:8080/x"},
		{"ipv4", "http://127.0.0.1/x"},
		{"single label", "https://example/"},
		{"scheme only", "https://"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := urlutil.Registrable(tc.input)
			assert.Empty(t, got, "Registrable(%q)", tc.input)
			require.ErrorIs(t, err, urlutil.ErrInvalid, "Registrable(%q)", tc.input)
		})
	}
}
