package signing

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSRFDisallowedIPs(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":    true,  // loopback
		"10.0.0.1":     true,  // RFC 1918
		"172.16.5.7":   true,  // RFC 1918
		"192.168.1.1":  true,  // RFC 1918
		"169.254.1.1":  true,  // link-local
		"100.64.0.1":   true,  // CGNAT
		"::1":          true,  // IPv6 loopback
		"fc00::1":      true,  // IPv6 ULA
		"fe80::1":      true,  // IPv6 link-local
		"8.8.8.8":      false, // public
		"1.1.1.1":      false, // public
		"2001:db8::1":  false, // documentation range (treated as public here)
	}
	for ipStr, want := range cases {
		ip := parseIP(t, ipStr)
		got := isDisallowedIP(ip)
		assert.Equalf(t, want, got, "%s", ipStr)
	}
}

func parseIP(t *testing.T, s string) (ip net.IP) {
	t.Helper()
	ip = net.ParseIP(s)
	require.NotNil(t, ip, "parse %s", s)
	return ip
}

// TestHTTPJWKSResolverFetchesFromLocalServer verifies the resolver integration
// with a real HTTP server.
func TestHTTPJWKSResolverFetchesFromLocalServer(t *testing.T) {
	// Generate a key, publish it via a local JWKS endpoint, resolve through the
	// fetcher. Disable SSRF on the fetcher's client since httptest uses loopback.
	res, err := GenerateSigningKey(AlgEd25519, "kid-1")
	require.NoError(t, err)
	jwks := JWKS{Keys: []JWK{res.PublicJWK}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resolver := &HTTPJWKSResolver{
		Agents:      map[string]string{"https://agent.example.com": srv.URL + "/.well-known/jwks.json"},
		HTTPClient:  srv.Client(), // bypasses SSRF filter for loopback
		cache:       map[string]*jwksCacheEntry{},
		lastRefetch: map[string]time.Time{},
	}

	jwk, agent, err := resolver.Resolve(context.Background(), "kid-1")
	require.NoError(t, err)
	assert.Equal(t, "kid-1", jwk.Kid)
	assert.Equal(t, "https://agent.example.com", agent)

	// Miss a kid not in the JWKS — with the cooldown already advanced.
	_, _, err = resolver.Resolve(context.Background(), "unknown-kid")
	require.Error(t, err)
	e := AsError(err)
	require.NotNil(t, e)
	assert.Equal(t, CodeKeyUnknown, e.Code)
}
