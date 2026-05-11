package urlutil_test

import (
	"errors"
	"testing"

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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := urlutil.Registrable(tc.input)
			if err != nil {
				t.Fatalf("Registrable(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("Registrable(%q) = %q, want %q", tc.input, got, tc.want)
			}
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
			if err == nil {
				t.Fatalf("Registrable(%q) = %q, want error", tc.input, got)
			}
			if !errors.Is(err, urlutil.ErrInvalid) {
				t.Errorf("Registrable(%q) error %v, want errors.Is(err, ErrInvalid)", tc.input, err)
			}
		})
	}
}
