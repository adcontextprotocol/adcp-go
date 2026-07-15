package tmproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// buildJWK generates an Ed25519 key and returns its public JWK-shaped
// SigningKey plus the private key so tests can sign locally.
func buildJWK(t *testing.T, kid string) (SigningKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return PublicSigningKey(kid, pub), priv
}

// newAuthzServer stands up a fake authorizations endpoint that records
// each request and returns whatever the handler function chooses. The
// handler receives the raw agent_url query param so tests can assert
// canonicalization was applied by the store.
func newAuthzServer(t *testing.T, handler func(agentURL string, w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentURL := r.URL.Query().Get("agent_url")
		if agentURL == "" {
			http.Error(w, "agent_url is required", http.StatusBadRequest)
			return
		}
		handler(agentURL, w, r)
	}))
}

func TestLazyAuthorizationKeyStore_FetchAndCache(t *testing.T) {
	jwk, _ := buildJWK(t, "kid-a")

	var calls int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{
				{"signing_keys": []SigningKey{jwk}},
			},
		})
	})
	defer srv.Close()

	ks, err := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example.com/agent")
	if !ok || got == nil || got.Kid != "kid-a" {
		t.Fatalf("first lookup: got=%+v ok=%v", got, ok)
	}

	// Cache hit — no additional HTTP call.
	got2, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example.com/agent")
	if !ok || got2 == nil {
		t.Fatal("second lookup missed the cache")
	}
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("expected 1 HTTP call, got %d", c)
	}
}

func TestLazyAuthorizationKeyStore_UnknownKidOnKnownAgent(t *testing.T) {
	jwk, _ := buildJWK(t, "kid-a")
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
	})

	_, ok := ks.LookupKeyForAgent("kid-b", "https://seller.example.com/agent")
	if ok {
		t.Error("expected kid-b to be unknown for this agent")
	}
}

func TestLazyAuthorizationKeyStore_NegativeCacheOn404(t *testing.T) {
	var calls int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
		NegativeTTL:         time.Second,
	})

	for i := 0; i < 3; i++ {
		if _, ok := ks.LookupKeyForAgent("kid", "https://nobody.example/agent"); ok {
			t.Fatalf("iteration %d: expected miss on unknown agent", i)
		}
	}
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("expected 1 HTTP call (rest from negative cache); got %d", c)
	}
}

func TestLazyAuthorizationKeyStore_TTLExpiration(t *testing.T) {
	jwk, _ := buildJWK(t, "kid-a")
	var calls int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
		PositiveTTL:         50 * time.Millisecond,
	})

	if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); !ok {
		t.Fatal("first fetch failed")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); !ok {
		t.Fatal("second fetch after TTL expiry failed")
	}
	if c := atomic.LoadInt32(&calls); c != 2 {
		t.Errorf("expected 2 HTTP calls after TTL expiry; got %d", c)
	}
}

func TestLazyAuthorizationKeyStore_BearerHeaderSent(t *testing.T) {
	var sawAuth string
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": []any{}})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		BearerToken:         "tk-42",
		AllowInsecureScheme: true,
	})
	_, _ = ks.LookupKeyForAgent("kid", "https://seller.example/agent")

	if sawAuth != "Bearer tk-42" {
		t.Errorf("Authorization header = %q, want %q", sawAuth, "Bearer tk-42")
	}
}

func TestLazyAuthorizationKeyStore_URLCanonicalization(t *testing.T) {
	// The fake server sees exactly what the store sent. If canonicalization
	// happened, uppercase host + trailing slash should be normalized before
	// being placed in the query string.
	var sawAgent string
	srv := newAuthzServer(t, func(agentURL string, w http.ResponseWriter, _ *http.Request) {
		sawAgent = agentURL
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": []any{}})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
	})
	_, _ = ks.LookupKeyForAgent("kid", "HTTPS://Seller.Example.COM/Agent/")

	if strings.Contains(sawAgent, "Seller.Example.COM") {
		t.Errorf("agent_url query still carries un-canonical host: %q", sawAgent)
	}
	if !strings.HasPrefix(sawAgent, "https://") {
		t.Errorf("agent_url query should start with lowercased scheme; got %q", sawAgent)
	}
}

func TestLazyAuthorizationKeyStore_SingleFlight(t *testing.T) {
	jwk, _ := buildJWK(t, "kid-a")
	var calls int32
	release := make(chan struct{})
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
	})

	const goroutines = 10
	var wg sync.WaitGroup
	var hits int32
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); ok {
				atomic.AddInt32(&hits, 1)
			}
		}()
	}
	// Give goroutines a moment to enqueue on the inflight fetch before
	// releasing the server response.
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != goroutines {
		t.Errorf("expected %d successful lookups, got %d", goroutines, got)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 HTTP call under single-flight; got %d", got)
	}
}

func TestLazyAuthorizationKeyStore_InvalidateForcesRefetch(t *testing.T) {
	jwk, _ := buildJWK(t, "kid-a")
	var calls int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
	})

	_, _ = ks.LookupKeyForAgent("kid-a", "https://seller.example/agent")
	ks.Invalidate("https://seller.example/agent")
	_, _ = ks.LookupKeyForAgent("kid-a", "https://seller.example/agent")

	if c := atomic.LoadInt32(&calls); c != 2 {
		t.Errorf("expected 2 HTTP calls after Invalidate; got %d", c)
	}
}

func TestLazyAuthorizationKeyStore_RejectsInsecureSchemeByDefault(t *testing.T) {
	_, err := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL: "http://example.com/api/registry/authorizations",
	})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("expected https-required error, got %v", err)
	}
}

// Verify* end-to-end: the store implements AgentAwareKeyStore so
// resolveSigningKey routes the lookup with req.SellerAgentURL, and
// VerifyContextMatch succeeds against a locally-signed request.
func TestLazyAuthorizationKeyStore_VerifyContextMatchEndToEnd(t *testing.T) {
	jwk, priv := buildJWK(t, "kid-e2e")
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, err := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	signer, err := NewSigner("kid-e2e", priv)
	if err != nil {
		t.Fatal(err)
	}
	req := &ContextMatchRequest{
		Type:           "context_match_request",
		RequestID:      "req-1",
		PropertyRID:    "01916f3a-1234-7000-8000-000000000001",
		PlacementID:    "slot-1",
		SellerAgentURL: "https://seller.example/agent",
	}
	endpoint := "https://provider.example/context"
	now := time.Now()
	sig := signer.SignContextMatch(req, endpoint, EpochAt(now))

	if err := VerifyContextMatch(req, endpoint, sig, "kid-e2e", ks, now); err != nil {
		t.Fatalf("VerifyContextMatch failed: %v", err)
	}
}
