package tmproto

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newJWKSStoreOnTestServer(t *testing.T, srv *httptest.Server) *JWKSStore {
	t.Helper()
	s, err := NewJWKSStore(JWKSStoreOptions{
		URL:                 srv.URL,
		AllowInsecureScheme: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJWKSStore_ParsesProductionShape(t *testing.T) {
	// Vector from api.staging.interchange.io/.well-known/jwks.json,
	// trimmed to one signing + one encryption key.
	body := `{"keys":[
  {"kid":"scope3-req-sign-staging","kty":"OKP","crv":"Ed25519","x":"GwUUztNpkwWtzOErcNqSTp8i0ctCfMG4WFeZmItkJ4k","use":"sig","alg":"EdDSA","key_ops":["verify"],"adcp_use":"request-signing"},
  {"kid":"d78GK3dc","kty":"OKP","crv":"X25519","x":"ArNfJ5QFYNxnopIuDail_FJ_k_fsECmB3xPUBGM2_GM","use":"enc","alg":"HPKE-DHKEM-X25519-HKDF-SHA256","adcp_use":"tmpx-encrypt","iat":1778179546}
]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	ks := newJWKSStoreOnTestServer(t, srv)
	if err := ks.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, ok := ks.LookupKey("scope3-req-sign-staging"); !ok {
		t.Error("signing key lookup miss")
	}
	rcp, ok := ks.CurrentEncryptionRecipient()
	if !ok {
		t.Fatal("encryption recipient miss")
	}
	if rcp.Kid != "d78GK3dc" {
		t.Errorf("kid=%q, want d78GK3dc", rcp.Kid)
	}
	if rcp.PublicKey.Curve() != ecdh.X25519() {
		t.Errorf("public key curve = %v, want X25519", rcp.PublicKey.Curve())
	}
}

func TestJWKSStore_PicksMostRecentEncryptionKeyByIAT(t *testing.T) {
	older := mustGenerateEncKey(t)
	newer := mustGenerateEncKey(t)
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]any{
			{"kid": "older", "kty": "OKP", "crv": "X25519", "x": older.b64x, "use": "enc", "alg": JWKSAlgEncryptionDHKEMX25519, "adcp_use": "tmpx-encrypt", "iat": 100},
			{"kid": "newer", "kty": "OKP", "crv": "X25519", "x": newer.b64x, "use": "enc", "alg": JWKSAlgEncryptionDHKEMX25519, "adcp_use": "tmpx-encrypt", "iat": 999},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	ks := newJWKSStoreOnTestServer(t, srv)
	if err := ks.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	rcp, ok := ks.CurrentEncryptionRecipient()
	if !ok {
		t.Fatal("no recipient")
	}
	if rcp.Kid != "newer" {
		t.Errorf("kid=%q, want newer (higher iat wins)", rcp.Kid)
	}
}

func TestJWKSStore_SkipsKeysWithWrongAlgOrCurve(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]any{
			// Wrong curve for signing.
			{"kid": "bad-sig", "kty": "OKP", "crv": "X25519", "x": "AAAA", "use": "sig", "alg": "EdDSA", "adcp_use": "request-signing"},
			// Wrong alg for encryption.
			{"kid": "bad-enc", "kty": "OKP", "crv": "X25519", "x": "AAAA", "use": "enc", "alg": "wrong-alg", "adcp_use": "tmpx-encrypt"},
			// Unknown adcp_use — forward-compat skip.
			{"kid": "future", "kty": "OKP", "crv": "Ed25519", "x": "AAAA", "adcp_use": "future-purpose"},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	ks := newJWKSStoreOnTestServer(t, srv)
	if err := ks.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.LookupKey("bad-sig"); ok {
		t.Error("wrong-curve signing key must be skipped")
	}
	if _, ok := ks.CurrentEncryptionRecipient(); ok {
		t.Error("wrong-alg encryption key must be skipped")
	}
}

func TestJWKSStore_LookupKeyForJWKSSignedRequests(t *testing.T) {
	// Demonstrate that a Signer can produce a token that the JWKS-backed
	// keystore verifies — the JWKS-published key format roundtrips with
	// VerifyContextMatch.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]any{
			{
				"kid": "kid-x", "kty": "OKP", "crv": "Ed25519",
				"x":   PublicSigningKey("kid-x", pub).X,
				"use": "sig", "alg": "EdDSA",
				"adcp_use": "request-signing",
			},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	ks := newJWKSStoreOnTestServer(t, srv)
	if err := ks.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	signer, _ := NewSigner("kid-x", priv)
	req := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl"}
	now := time.Now()
	sig := signer.SignContextMatch(req, "https://prov", EpochAt(now))
	if err := VerifyContextMatch(req, "https://prov", sig, "kid-x", ks, now); err != nil {
		t.Fatalf("JWKS-backed verify failed: %v", err)
	}
}

func TestJWKSStore_RejectsInsecureSchemeByDefault(t *testing.T) {
	_, err := NewJWKSStore(JWKSStoreOptions{URL: "http://example.com/jwks.json"})
	if err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("plain http URL must be rejected by default, got %v", err)
	}
}

func TestJWKSStore_RejectsBadScheme(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "gopher://x"} {
		_, err := NewJWKSStore(JWKSStoreOptions{URL: u, AllowInsecureScheme: true})
		if err == nil {
			t.Errorf("URL %q should be rejected", u)
		}
	}
}

func TestJWKSStore_EmptyJWKSRetainsCachedKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	enc := mustGenerateEncKey(t)
	var serveEmpty bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if serveEmpty {
			_, _ = w.Write([]byte(`{"keys":[]}`))
			return
		}
		body, _ := json.Marshal(map[string]any{
			"keys": []map[string]any{
				{"kid": "sig-1", "kty": "OKP", "crv": "Ed25519", "x": PublicSigningKey("sig-1", pub).X,
					"use": "sig", "alg": "EdDSA", "adcp_use": "request-signing"},
				{"kid": "enc-1", "kty": "OKP", "crv": "X25519", "x": enc.b64x,
					"use": "enc", "alg": JWKSAlgEncryptionDHKEMX25519, "adcp_use": "tmpx-encrypt", "iat": 1},
			},
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	ks := newJWKSStoreOnTestServer(t, srv)
	if err := ks.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.LookupKey("sig-1"); !ok {
		t.Fatal("seed miss")
	}
	serveEmpty = true
	if err := ks.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.LookupKey("sig-1"); !ok {
		t.Error("empty JWKS should retain cached signing keys")
	}
	if _, ok := ks.CurrentEncryptionRecipient(); !ok {
		t.Error("empty JWKS should retain cached encryption recipient")
	}
}

type encKeyFixture struct {
	skR  *ecdh.PrivateKey
	b64x string
}

func mustGenerateEncKey(t *testing.T) encKeyFixture {
	t.Helper()
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return encKeyFixture{
		skR:  sk,
		b64x: base64.RawURLEncoding.EncodeToString(sk.PublicKey().Bytes()),
	}
}
