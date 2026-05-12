package tmproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mkVerifier(t *testing.T, requireSig bool, ownEndpoint string) (http.Handler, *Signer, *bytes.Buffer) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner("kid-mw", priv)
	if err != nil {
		t.Fatal(err)
	}
	ks := NewStaticKeyStore([]SigningKey{PublicSigningKey(signer.KeyID, pub)})

	innerCalls := &bytes.Buffer{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		innerCalls.Write(body)
		w.WriteHeader(http.StatusOK)
	})
	mw := VerifyContextMatchHandler(inner, VerifyOptions{
		KeyStore:         ks,
		OwnEndpointURL:   ownEndpoint,
		RequireSignature: requireSig,
	})
	return mw, signer, innerCalls
}

func TestMiddleware_ContextMatchHappyPath(t *testing.T) {
	mw, signer, innerCalls := mkVerifier(t, true, "https://provider.example.com")

	body := []byte(`{"request_id":"r1","property_id":"p","property_rid":"rid","property_type":"website","placement_id":"sb","package_ids":["a"]}`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	parsed := &ContextMatchRequest{
		RequestID:    "r1",
		PropertyID:   "p",
		PropertyRID:  "rid",
		PropertyType: "website",
		PlacementID:  "sb",
		PackageIDs:   []string{"a"},
	}
	sig := signer.SignContextMatch(parsed, "https://provider.example.com", CurrentEpoch())
	req.Header.Set(HeaderTMPSignature, sig)
	req.Header.Set(HeaderTMPKeyID, signer.KeyID)

	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Inner handler must have received the original body intact.
	if !bytes.Equal(innerCalls.Bytes(), body) {
		t.Fatalf("inner body = %q, want %q", innerCalls.Bytes(), body)
	}
}

func TestMiddleware_RequireSignatureMissing(t *testing.T) {
	mw, _, innerCalls := mkVerifier(t, true, "https://provider.example.com")
	req, _ := http.NewRequest("POST", "/tmp/context",
		bytes.NewReader([]byte(`{"request_id":"r","property_rid":"p","placement_id":"s"}`)))
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if innerCalls.Len() != 0 {
		t.Fatal("inner handler should not have been called")
	}
}

func TestMiddleware_AllowUnsigned(t *testing.T) {
	mw, _, innerCalls := mkVerifier(t, false, "https://provider.example.com")
	body := []byte(`{"request_id":"r","property_rid":"p","placement_id":"s"}`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !bytes.Equal(innerCalls.Bytes(), body) {
		t.Fatalf("inner body = %q, want %q", innerCalls.Bytes(), body)
	}
}

func TestMiddleware_BadSignatureRejects(t *testing.T) {
	mw, _, _ := mkVerifier(t, true, "https://provider.example.com")
	body := []byte(`{"request_id":"r","property_rid":"p","placement_id":"s"}`)
	req, _ := http.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	req.Header.Set(HeaderTMPSignature, "AAAAAA")
	req.Header.Set(HeaderTMPKeyID, "kid-mw")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var resp ErrorResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Code == "" {
		t.Fatal("expected error code in response body")
	}
}
