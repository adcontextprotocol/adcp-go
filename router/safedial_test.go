package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safeDialContext resolves the URL host to an IP and dials the IP
// directly. That leaves an open question every reader has to answer for
// themselves: does the TLS handshake still see the ORIGINAL hostname as
// its SNI/ServerName, or does it try to verify the IP against the
// cert (which would fail for any real HTTPS provider whose cert is
// bound to a hostname)?
//
// Go's stdlib http.Transport derives the TLS ServerName from the URL
// host via cm.tlsHost() (net/http/transport.go:2086), independent of
// what DialContext returns — so SNI is preserved. This test locks that
// behavior in as a regression guard: if a future change accidentally
// makes the dial-target address flow into ServerName, the TLS handshake
// against a hostname-only cert will fail and this test breaks loudly.
func TestSafeDialContext_PreservesTLSSNI(t *testing.T) {
	// httptest.NewTLSServer binds to 127.0.0.1 and generates a cert
	// with SAN entries for "example.com" and "127.0.0.1". We connect
	// using URL host "example.com" and remap it to 127.0.0.1 via a
	// dial wrapper that mirrors safeDialContext's behavior (dial the
	// resolved IP, not the URL host). If ServerName came from the
	// dial address, TLS would send SNI=127.0.0.1 and the server's
	// cert response would still verify — hiding any regression. To
	// force the test to depend on SNI, restrict the cert's SANs to
	// the hostname only and drop the IP SAN.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	// Take httptest's auto-generated cert and rewrite it so only
	// "example.com" is a valid SAN — no IPs. Now the server can only
	// be reached over TLS by a client that sends SNI=example.com.
	cert, key := hostOnlyCert(t, "example.com")
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{mustX509KeyPair(t, cert, key)}}
	srv.StartTLS()
	defer srv.Close()

	// Extract 127.0.0.1:PORT from srv.URL and reuse the port to talk
	// to the server through URL host "example.com".
	loopbackAddr, err := url.Parse(srv.URL)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(loopbackAddr.Host)
	require.NoError(t, err)

	rootPool := x509.NewCertPool()
	rootPool.AddCert(srv.Certificate())

	// Dial wrapper: for any host, rewrite the dial target to
	// 127.0.0.1:PORT — mirroring safeDialContext's resolve-then-dial
	// shape. Under Go's http.Transport this must NOT leak into the
	// TLS ServerName.
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+port)
		},
		TLSClientConfig: &tls.Config{RootCAs: rootPool},
	}
	client := &http.Client{Transport: transport}

	resp, err := client.Get("https://example.com/")
	require.NoError(t, err, "SNI must be derived from URL host so the hostname-only cert verifies")
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
}

// hostOnlyCert produces a self-signed TLS cert whose only SAN is the
// hostname (no IP entries), forcing any successful TLS handshake to
// depend on SNI = hostname.
func hostOnlyCert(t *testing.T, host string) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func mustX509KeyPair(t *testing.T, certPEM, keyPEM []byte) tls.Certificate {
	t.Helper()
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return cert
}

// TestSafeDialContext_RejectsPrivateIP proves the core rebinding
// defense: if DNS resolves to a private range the dial must fail.
func TestSafeDialContext_RejectsPrivateIP(t *testing.T) {
	// 10.0.0.1 is a private-range IP that safeDialContext should
	// reject at the validation gate. We can't use a hostname here
	// without going through the real resolver — dialing "10.0.0.1:1"
	// directly exercises the SplitHostPort → LookupIPAddr → validate
	// path (LookupIPAddr on a literal IP returns that IP unchanged).
	_, err := safeDialContext(context.Background(), "tcp", "10.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "not allowed",
		"private-range dial must be blocked by safeDialContext")
}
