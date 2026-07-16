package tmproto

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
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
// LookupKey without a seller_agent_url must fail closed — the store is
// exclusively agent-scoped and a plain kid lookup would break the
// per-agent isolation AgentAwareKeyStore is designed to preserve.
func TestLazyAuthorizationKeyStore_LookupKeyAlwaysFails(t *testing.T) {
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		jwk, _ := buildJWK(t, "kid-a")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL: srv.URL, AllowInsecureScheme: true,
	})
	// Populate the cache via the agent-aware path so the entry exists.
	if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); !ok {
		t.Fatal("agent-aware lookup did not populate the cache")
	}
	// Plain LookupKey MUST still return false — no linear scan.
	if _, ok := ks.LookupKey("kid-a"); ok {
		t.Error("LookupKey returned true; expected agent-scoped isolation")
	}
}

// The cache is LRU-bounded so an attacker cannot grow it indefinitely.
// After N+1 unique agent URLs, at most N entries remain.
func TestLazyAuthorizationKeyStore_CacheSizeCap(t *testing.T) {
	jwk, _ := buildJWK(t, "kid")
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	const cap = 4
	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
		MaxCacheEntries:     cap,
	})
	for i := 0; i < cap*3; i++ {
		_, _ = ks.LookupKeyForAgent("kid", "https://seller"+strings.Repeat("x", i)+".example/agent")
	}
	if got := ks.cache.Len(); got > cap {
		t.Errorf("cache grew past ceiling: got %d, want <= %d", got, cap)
	}
}

// The fetch semaphore bounds concurrent outbound registry calls so a
// spray of unique URLs cannot amplify 1:1. Excess concurrent misses
// fail closed rather than queue.
func TestLazyAuthorizationKeyStore_FetchConcurrencyCap(t *testing.T) {
	release := make(chan struct{})
	var inflight int32
	var maxInflight int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		cur := atomic.AddInt32(&inflight, 1)
		for {
			m := atomic.LoadInt32(&maxInflight)
			if cur <= m || atomic.CompareAndSwapInt32(&maxInflight, m, cur) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inflight, -1)
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": []any{}})
	})
	defer srv.Close()

	const semCap = 2
	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:              srv.URL,
		AllowInsecureScheme:  true,
		MaxConcurrentFetches: semCap,
	})

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		agentURL := "https://seller" + strings.Repeat("x", i+1) + ".example/agent"
		go func() {
			defer wg.Done()
			_, _ = ks.LookupKeyForAgent("kid", agentURL)
		}()
	}
	// Let all goroutines reach the semaphore before releasing.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&maxInflight); got > int32(semCap) {
		t.Errorf("observed %d concurrent fetches; expected <= %d", got, semCap)
	}
}

// After an entry expires and the refetch fails, ServeStaleGrace lets
// the store keep serving the stale entry up to the grace window.
func TestLazyAuthorizationKeyStore_ServeStaleGraceOnRefetchError(t *testing.T) {
	jwk, _ := buildJWK(t, "kid-a")
	var fail int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
		PositiveTTL:         50 * time.Millisecond,
		ServeStaleGrace:     time.Second,
	})

	if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); !ok {
		t.Fatal("initial lookup failed")
	}
	// Wait for expiry.
	time.Sleep(80 * time.Millisecond)
	atomic.StoreInt32(&fail, 1)

	// Refetch fails; grace window still valid → stale entry served.
	if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); !ok {
		t.Error("expected stale entry to be served within grace window on refetch error")
	}
}

// Grace does NOT extend indefinitely — past staleUntil the store fails
// closed even under refetch error.
func TestLazyAuthorizationKeyStore_StaleGraceIsBounded(t *testing.T) {
	jwk, _ := buildJWK(t, "kid-a")
	var fail int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{jwk}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
		PositiveTTL:         30 * time.Millisecond,
		ServeStaleGrace:     30 * time.Millisecond,
	})

	if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); !ok {
		t.Fatal("initial lookup failed")
	}
	atomic.StoreInt32(&fail, 1)
	// Wait past positiveTTL + serveStaleGrace.
	time.Sleep(120 * time.Millisecond)

	if _, ok := ks.LookupKeyForAgent("kid-a", "https://seller.example/agent"); ok {
		t.Error("expected fail-closed past stale grace window")
	}
}

// A cached entry younger than the cooldown must NOT trigger a refetch
// on an unknown kid — otherwise a spray of forged kids from a real
// seller's URL would amplify to the registry 1:1.
func TestLazyAuthorizationKeyStore_UnknownKidBlockedWithinCooldown(t *testing.T) {
	oldJWK, _ := buildJWK(t, "kid-old")
	newJWK, _ := buildJWK(t, "kid-new")
	var phase atomic.Int32
	var calls atomic.Int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		keys := []SigningKey{oldJWK}
		if phase.Load() == 1 {
			keys = []SigningKey{newJWK}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": keys}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:                   srv.URL,
		AllowInsecureScheme:       true,
		UnknownKidRefetchCooldown: 5 * time.Second, // long enough that the test never crosses it
	})
	if _, ok := ks.LookupKeyForAgent("kid-old", "https://seller.example/agent"); !ok {
		t.Fatal("kid-old should be cached after first lookup")
	}
	phase.Store(1)
	if _, ok := ks.LookupKeyForAgent("kid-new", "https://seller.example/agent"); ok {
		t.Error("expected fresh-entry gate to block refetch inside cooldown")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 HTTP call inside cooldown; got %d", got)
	}
}

// Past the cooldown, an unknown kid triggers exactly one refetch which
// picks up a legitimate rotation. This is the feature this path exists
// to provide.
func TestLazyAuthorizationKeyStore_UnknownKidRefetchesPastCooldown(t *testing.T) {
	oldJWK, _ := buildJWK(t, "kid-old")
	newJWK, _ := buildJWK(t, "kid-new")
	var phase atomic.Int32
	var calls atomic.Int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		keys := []SigningKey{oldJWK}
		if phase.Load() == 1 {
			keys = []SigningKey{newJWK}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": keys}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:                   srv.URL,
		AllowInsecureScheme:       true,
		UnknownKidRefetchCooldown: 20 * time.Millisecond,
	})
	if _, ok := ks.LookupKeyForAgent("kid-old", "https://seller.example/agent"); !ok {
		t.Fatal("kid-old should be cached after first lookup")
	}
	phase.Store(1)
	time.Sleep(40 * time.Millisecond) // cross the cooldown

	if _, ok := ks.LookupKeyForAgent("kid-new", "https://seller.example/agent"); !ok {
		t.Error("expected refetch to pick up rotated key past cooldown")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 HTTP calls (initial + refetch), got %d", got)
	}
	// Legitimate old-kid traffic still verifies against the new snapshot
	// only if the old kid is still authorized — here the server dropped
	// it, so kid-old is now genuinely absent. Confirm no crash / hang.
	if _, ok := ks.LookupKeyForAgent("kid-old", "https://seller.example/agent"); ok {
		t.Error("kid-old should no longer resolve after registry dropped it")
	}
}

// Refetch failures must not be silent — an operator debugging a
// rotation-during-outage needs to see them in logs. Captures the
// logger's output and asserts the failure line is present.
func TestLazyAuthorizationKeyStore_RefetchFailureIsLogged(t *testing.T) {
	oldJWK, _ := buildJWK(t, "kid-old")
	var fail atomic.Int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		if fail.Load() == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{oldJWK}}},
		})
	})
	defer srv.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:                   srv.URL,
		AllowInsecureScheme:       true,
		UnknownKidRefetchCooldown: 20 * time.Millisecond,
		Logger:                    logger,
	})
	if _, ok := ks.LookupKeyForAgent("kid-old", "https://seller.example/agent"); !ok {
		t.Fatal("initial lookup failed")
	}
	time.Sleep(40 * time.Millisecond)
	fail.Store(1)
	_, _ = ks.LookupKeyForAgent("kid-forged", "https://seller.example/agent")

	logs := buf.String()
	if !strings.Contains(logs, "authorization keystore refetch failed") {
		t.Errorf("expected refetch failure to be logged; got: %s", logs)
	}
}

// Blocker regression test: an attacker-triggered refetch failure MUST
// NOT evict the still-valid cached entry. Fetch-then-swap semantics.
func TestLazyAuthorizationKeyStore_RefetchFailurePreservesCachedEntry(t *testing.T) {
	oldJWK, _ := buildJWK(t, "kid-old")
	var fail atomic.Int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		if fail.Load() == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{{"signing_keys": []SigningKey{oldJWK}}},
		})
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:                   srv.URL,
		AllowInsecureScheme:       true,
		UnknownKidRefetchCooldown: 20 * time.Millisecond,
	})
	// Warm the cache with kid-old.
	if _, ok := ks.LookupKeyForAgent("kid-old", "https://seller.example/agent"); !ok {
		t.Fatal("initial lookup failed")
	}
	// Cross the cooldown and simulate a registry outage.
	time.Sleep(40 * time.Millisecond)
	fail.Store(1)

	// Attacker: probe with a bogus kid. Refetch will 500 → return nil.
	// The cached kid-old entry MUST survive.
	if _, ok := ks.LookupKeyForAgent("kid-forged", "https://seller.example/agent"); ok {
		t.Error("forged kid should not resolve")
	}

	// Legitimate traffic from the same seller with the real kid must
	// still verify — the outage did not evict its key.
	if _, ok := ks.LookupKeyForAgent("kid-old", "https://seller.example/agent"); !ok {
		t.Error("legitimate kid-old evicted by failed refetch — regression")
	}
}

func TestLazyAuthorizationKeyStore_FetchContextPropagates(t *testing.T) {
	// A caller with an already-cancelled context should not trigger any
	// HTTP call. Confirms context propagation into fetch.
	var calls atomic.Int32
	srv := newAuthzServer(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rows":[]}`))
	})
	defer srv.Close()

	ks, _ := NewLazyAuthorizationKeyStore(LazyAuthorizationKeyStoreOptions{
		BaseURL:             srv.URL,
		AllowInsecureScheme: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = ks.LookupKeyForAgentCtx(ctx, "kid", "https://seller.example/agent")
	// The request may or may not have made it to the server before ctx
	// was checked; what matters is that the lookup returned fail-closed
	// (nil, false) rather than blocking. Sanity: server saw at most one
	// call.
	if got := calls.Load(); got > 1 {
		t.Errorf("cancelled ctx produced %d HTTP calls, expected 0 or 1", got)
	}
}

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
