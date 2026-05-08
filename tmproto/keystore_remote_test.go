package tmproto

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteKeyStore_RefreshAndLookup(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := PublicSigningKey("kid-from-router", pub)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"properties": []map[string]any{
				{
					"property_id":  "p1",
					"property_rid": "rid-1",
					"signing_keys": []SigningKey{jwk},
				},
			},
		})
	}))
	defer srv.Close()

	ks, err := NewRemoteKeyStore(RemoteKeyStoreOptions{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	n, err := ks.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 key, got %d", n)
	}
	got, ok := ks.LookupKey("kid-from-router")
	if !ok {
		t.Fatal("lookup miss")
	}
	if got.Kid != jwk.Kid {
		t.Fatalf("kid = %q", got.Kid)
	}
}

func TestRemoteKeyStore_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ks, _ := NewRemoteKeyStore(RemoteKeyStoreOptions{URL: srv.URL})
	if _, err := ks.Refresh(context.Background()); err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}
