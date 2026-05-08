package tmproto

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestKeyStore(t *testing.T, srv *httptest.Server) *RemoteKeyStore {
	t.Helper()
	ks, err := NewRemoteKeyStore(RemoteKeyStoreOptions{
		URL:                 srv.URL,
		AllowInsecureScheme: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

func TestRemoteKeyStore_RefreshAndLookup(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := PublicSigningKey("kid-from-router", pub)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	ks := newTestKeyStore(t, srv)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ks := newTestKeyStore(t, srv)
	if _, err := ks.Refresh(context.Background()); err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestRemoteKeyStore_RejectsInsecureSchemeByDefault(t *testing.T) {
	_, err := NewRemoteKeyStore(RemoteKeyStoreOptions{URL: "http://example.com/snap"})
	if err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("plain http URL must be rejected by default, got %v", err)
	}
}

func TestRemoteKeyStore_RejectsBadScheme(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com", "gopher://x"} {
		_, err := NewRemoteKeyStore(RemoteKeyStoreOptions{URL: u, AllowInsecureScheme: true})
		if err == nil {
			t.Errorf("URL %q should be rejected", u)
		}
	}
}

func TestRemoteKeyStore_AllowInsecureScheme(t *testing.T) {
	_, err := NewRemoteKeyStore(RemoteKeyStoreOptions{URL: "http://example.com/snap", AllowInsecureScheme: true})
	if err != nil {
		t.Fatalf("AllowInsecureScheme should permit http://: %v", err)
	}
}

func TestRemoteKeyStore_DeniesCrossOriginRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"properties": []any{}})
	}))
	defer other.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, other.URL+"/snapshot", http.StatusFound)
	}))
	defer src.Close()

	ks := newTestKeyStore(t, src)
	_, err := ks.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("cross-origin redirect must be rejected, got %v", err)
	}
}

func TestRemoteKeyStore_EmptySnapshotRetainsCachedKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	jwk := PublicSigningKey("kid-1", pub)
	emit := []SigningKey{jwk}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"properties": []map[string]any{
				{"property_id": "p1", "property_rid": "rid-1", "signing_keys": emit},
			},
		})
	}))
	defer srv.Close()

	ks := newTestKeyStore(t, srv)
	if _, err := ks.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.LookupKey("kid-1"); !ok {
		t.Fatal("seed missed")
	}

	emit = nil
	n, err := ks.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected cached key count to survive empty snapshot, got %d", n)
	}
	if _, ok := ks.LookupKey("kid-1"); !ok {
		t.Fatal("cached key was wiped on empty snapshot")
	}
}

func TestParseRegistrySnapshot_KidCollisionAcrossPropertiesKeepsFirst(t *testing.T) {
	pubA, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)
	jwkA := PublicSigningKey("shared-kid", pubA)
	jwkB := PublicSigningKey("shared-kid", pubB)

	body, _ := json.Marshal(map[string]any{
		"properties": []map[string]any{
			{"property_id": "p1", "property_rid": "rid-1", "signing_keys": []SigningKey{jwkA}},
			{"property_id": "p2", "property_rid": "rid-2", "signing_keys": []SigningKey{jwkB}},
		},
	})
	keys, err := parseRegistrySnapshot(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("collision should reduce to one entry, got %d", len(keys))
	}
	got := keys["shared-kid"]
	if got.X != jwkA.X {
		t.Fatal("first-seen entry should win on kid collision")
	}
}

func TestParseRegistrySnapshot_SameKidSameProperty(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	jwk := PublicSigningKey("k", pub)

	body, _ := json.Marshal(map[string]any{
		"properties": []map[string]any{
			{"property_id": "p1", "property_rid": "rid-1", "signing_keys": []SigningKey{jwk, jwk}},
		},
	})
	keys, err := parseRegistrySnapshot(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("same kid same property is not a collision, got %d", len(keys))
	}
}

func TestRemoteKeyStore_RunRefreshesUntilContextCanceled(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	jwk := PublicSigningKey("kid-x", pub)
	fetched := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case fetched <- struct{}{}:
		default:
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"properties": []map[string]any{
				{"property_id": "p1", "property_rid": "rid-1", "signing_keys": []SigningKey{jwk}},
			},
		})
	}))
	defer srv.Close()

	ks := newTestKeyStore(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ks.Run(ctx) }()
	<-fetched
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from Run, got %v", err)
	}
}
