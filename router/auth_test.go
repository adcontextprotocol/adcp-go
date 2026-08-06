package router

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// testAuthKey is long enough to clear MinAuthAPIKeyLength.
const testAuthKey = "ZmFrZS1yb3V0ZXIta2V5LWZvci11bml0LXRlc3Rz"

type countingAuthMetrics struct {
	reasons []string
}

func (m *countingAuthMetrics) IncAuthRejected(reason string) {
	m.reasons = append(m.reasons, reason)
}

// authTestHandler records whether the protected handler was reached.
func authTestHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AuthConfig
		wantErr string
	}{
		{"disabled needs nothing", AuthConfig{Disabled: true}, ""},
		{"api key", AuthConfig{APIKeys: []string{testAuthKey}}, ""},
		{"client ca", AuthConfig{ClientCAPath: "/etc/tmp/ca.pem"}, ""},
		{"enabled with no mechanism", AuthConfig{}, "configure api_keys or client_ca_path"},
		{"short key", AuthConfig{APIKeys: []string{"short"}}, "minimum is 32"},
		{"bad header", AuthConfig{APIKeys: []string{testAuthKey}, KeyHeader: "X Bad Header"}, "not a valid HTTP header name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestAuthDisabledPassesThrough covers the explicit opt-out: NewInboundAuth
// returns nil and Middleware is transparent.
func TestAuthDisabledPassesThrough(t *testing.T) {
	auth, err := NewInboundAuth(AuthConfig{Disabled: true})
	require.NoError(t, err)
	require.Nil(t, auth)

	reached := false
	w := httptest.NewRecorder()
	auth.Middleware(authTestHandler(&reached)).ServeHTTP(w, httptest.NewRequest("POST", "/tmp/context", nil))

	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuthAcceptsBearerAndKeyHeader(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{"bearer", map[string]string{"Authorization": "Bearer " + testAuthKey}},
		{"bearer lowercase scheme", map[string]string{"Authorization": "bearer " + testAuthKey}},
		{"key header", map[string]string{DefaultAuthKeyHeader: testAuthKey}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := NewInboundAuth(AuthConfig{APIKeys: []string{testAuthKey}})
			require.NoError(t, err)

			reached := false
			req := httptest.NewRequest("POST", "/tmp/context", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			auth.Middleware(authTestHandler(&reached)).ServeHTTP(w, req)

			assert.True(t, reached, "valid credential must reach the handler")
			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

// TestAuthRejectsBadCredentials pins that an unauthenticated caller never
// reaches the fan-out — the point of the spec MUST is that the router's
// signature is not available to launder unauthenticated requests.
func TestAuthRejectsBadCredentials(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		wantReason string
	}{
		{"no credential", nil, AuthRejectMissingCredential},
		{"wrong key", map[string]string{DefaultAuthKeyHeader: "wrong-but-long-enough-key-value-here"}, AuthRejectInvalidKey},
		{"empty bearer", map[string]string{"Authorization": "Bearer "}, AuthRejectMissingCredential},
		{"wrong scheme", map[string]string{"Authorization": "Basic " + testAuthKey}, AuthRejectMissingCredential},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metrics := &countingAuthMetrics{}
			auth, err := NewInboundAuth(AuthConfig{APIKeys: []string{testAuthKey}}, WithAuthMetrics(metrics))
			require.NoError(t, err)

			reached := false
			req := httptest.NewRequest("POST", "/tmp/context", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			auth.Middleware(authTestHandler(&reached)).ServeHTTP(w, req)

			assert.False(t, reached, "handler must not run for an unauthenticated caller")
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "Bearer", w.Header().Get("WWW-Authenticate"))
			assert.Equal(t, []string{tc.wantReason}, metrics.reasons)

			var errResp tmproto.ErrorResponse
			require.NoError(t, json.NewDecoder(w.Body).Decode(&errResp))
			assert.Equal(t, tmproto.TypeError, errResp.Type)
			assert.Equal(t, tmproto.ErrorCodeInvalidRequest, errResp.Code)
		})
	}
}

// TestAuthAcceptsAnyConfiguredKey covers rotation: both the outgoing and the
// incoming secret work while the publisher migrates.
func TestAuthAcceptsAnyConfiguredKey(t *testing.T) {
	const second = "c2Vjb25kLXJvdXRlci1rZXktZm9yLXVuaXQtdGVzdHM"
	auth, err := NewInboundAuth(AuthConfig{APIKeys: []string{testAuthKey, second}})
	require.NoError(t, err)

	for _, key := range []string{testAuthKey, second} {
		reached := false
		req := httptest.NewRequest("POST", "/tmp/context", nil)
		req.Header.Set(DefaultAuthKeyHeader, key)
		auth.Middleware(authTestHandler(&reached)).ServeHTTP(httptest.NewRecorder(), req)
		assert.True(t, reached, "key %q should be accepted during rotation", key)
	}
}

// TestAuthRequiresClientCert covers the mTLS mechanism. A request with no TLS
// peer certificate is rejected even though the handshake would normally have
// enforced it — defense in depth against a misconfigured listener.
func TestAuthRequiresClientCert(t *testing.T) {
	caPath := writeTestCAPEM(t)
	metrics := &countingAuthMetrics{}
	auth, err := NewInboundAuth(AuthConfig{ClientCAPath: caPath}, WithAuthMetrics(metrics))
	require.NoError(t, err)

	reached := false
	w := httptest.NewRecorder()
	auth.Middleware(authTestHandler(&reached)).ServeHTTP(w, httptest.NewRequest("POST", "/tmp/context", nil))

	assert.False(t, reached)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, []string{AuthRejectMissingClientCert}, metrics.reasons)
	assert.Empty(t, w.Header().Get("WWW-Authenticate"), "no bearer challenge when only mTLS is configured")

	// An offered-but-unverified certificate must NOT pass. This is the shape a
	// listener produces under tls.RequestClientCert or RequireAnyClientCert:
	// PeerCertificates populated, VerifiedChains empty. Accepting it would let
	// any self-signed certificate reach the fan-out.
	reached = false
	unverified := httptest.NewRequest("POST", "/tmp/context", nil)
	unverified.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	w = httptest.NewRecorder()
	auth.Middleware(authTestHandler(&reached)).ServeHTTP(w, unverified)
	assert.False(t, reached, "an unverified client certificate must be rejected")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// A verified chain passes.
	reached = false
	verified := httptest.NewRequest("POST", "/tmp/context", nil)
	verified.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{}},
		VerifiedChains:   [][]*x509.Certificate{{{}}},
	}
	auth.Middleware(authTestHandler(&reached)).ServeHTTP(httptest.NewRecorder(), verified)
	assert.True(t, reached)
}

// TestAuthBothMechanismsAreRequired pins that configuring api_keys and mTLS
// together ANDs them. The config field docs read as alternatives ("or both"), so
// an operator layering mTLS onto a working key deployment needs this to be
// stated: every caller without a client certificate is locked out on restart.
func TestAuthBothMechanismsAreRequired(t *testing.T) {
	auth, err := NewInboundAuth(AuthConfig{
		APIKeys:      []string{testAuthKey},
		ClientCAPath: writeTestCAPEM(t),
	})
	require.NoError(t, err)

	verifiedTLS := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{}},
		VerifiedChains:   [][]*x509.Certificate{{{}}},
	}

	// Valid key, no client cert → rejected.
	reached := false
	keyOnly := httptest.NewRequest("POST", "/tmp/context", nil)
	keyOnly.Header.Set(DefaultAuthKeyHeader, testAuthKey)
	auth.Middleware(authTestHandler(&reached)).ServeHTTP(httptest.NewRecorder(), keyOnly)
	assert.False(t, reached, "a valid key alone must not satisfy a config that also requires mTLS")

	// Client cert, no key → rejected.
	reached = false
	certOnly := httptest.NewRequest("POST", "/tmp/context", nil)
	certOnly.TLS = verifiedTLS
	auth.Middleware(authTestHandler(&reached)).ServeHTTP(httptest.NewRecorder(), certOnly)
	assert.False(t, reached, "a client cert alone must not satisfy a config that also requires a key")

	// Both → accepted.
	reached = false
	both := httptest.NewRequest("POST", "/tmp/context", nil)
	both.TLS = verifiedTLS
	both.Header.Set(DefaultAuthKeyHeader, testAuthKey)
	auth.Middleware(authTestHandler(&reached)).ServeHTTP(httptest.NewRecorder(), both)
	assert.True(t, reached)
}

func TestClientCAPoolErrors(t *testing.T) {
	dir := t.TempDir()

	missing := AuthConfig{ClientCAPath: filepath.Join(dir, "nope.pem")}
	_, err := missing.ClientCAPool()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read client CA")

	notPEM := filepath.Join(dir, "garbage.pem")
	require.NoError(t, os.WriteFile(notPEM, []byte("not a pem"), 0o600))
	_, err = AuthConfig{ClientCAPath: notPEM}.ClientCAPool()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PEM certificates")

	keyInsteadOfCA := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(keyInsteadOfCA,
		[]byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), 0o600))
	_, err = AuthConfig{ClientCAPath: keyInsteadOfCA}.ClientCAPool()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expected CERTIFICATE`)

	none := AuthConfig{}
	pool, err := none.ClientCAPool()
	require.NoError(t, err)
	assert.Nil(t, pool)
}

// TestClientCAPoolDisabledYieldsNoPool pins that the opt-out also turns off
// mTLS. Loading the pool installs RequireAndVerifyClientCert on the listener, so
// a leftover client_ca_path would reject every publisher at the handshake after
// the operator moved authentication to a mesh — an outage the "authentication is
// disabled" startup log would actively hide.
func TestClientCAPoolDisabledYieldsNoPool(t *testing.T) {
	cfg := AuthConfig{Disabled: true, ClientCAPath: writeTestCAPEM(t)}

	pool, err := cfg.ClientCAPool()

	require.NoError(t, err)
	assert.Nil(t, pool, "auth.disabled must not leave mTLS enforced on the listener")
}

// TestServerConfigValidate_MTLSRequiresRouterTLS pins the cross-section rule:
// the client-cert trust anchor lives on the router's own listener, so mTLS with
// TLS terminated upstream would reject every request for a missing peer
// certificate. Catch it at startup instead.
func TestServerConfigValidate_MTLSRequiresRouterTLS(t *testing.T) {
	cfg := &ServerConfig{Auth: AuthConfig{ClientCAPath: "/etc/tmp/ca.pem"}}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_ca_path requires the router to terminate TLS")

	cfg.TLS = TLSConfig{CertPath: "/etc/tmp/tls.crt", KeyPath: "/etc/tmp/tls.key"}
	assert.NoError(t, cfg.Validate())
}

// TestServerConfigValidate_FailsClosedWithoutAuth pins that an operator cannot
// reach an unauthenticated router by omission — the spec requires publisher
// authentication, so opting out has to be explicit.
func TestServerConfigValidate_FailsClosedWithoutAuth(t *testing.T) {
	cfg := &ServerConfig{}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth")

	cfg.Auth.Disabled = true
	assert.NoError(t, cfg.Validate())
}

// writeTestCAPEM generates a self-signed CA certificate and returns its PEM
// path. Generated rather than checked in as a fixture so there is no embedded
// certificate to rotate.
func writeTestCAPEM(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tmp-router-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "ca.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	return path
}
