package uid2client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testClientSecretB64 is a randomly generated 32-byte AES key used only
// by unit tests, encoded as base64. No production system uses this value.
const testClientSecretB64 = "ioG3wKxAokmp+rERx6A4kM/13qhyolUXIu14WN16Spo=" //nolint:gosec // test fixture, not a real credential

// TestNew_InitialRefreshHappyPath spins up a fake operator that behaves
// like /v2/key/bidstream: decrypts the AES-GCM envelope, echoes the
// nonce, and returns a small keyset. Confirms New() blocks until the
// initial refresh completes and Decrypt then works against a
// generator-minted token.
func TestNew_InitialRefreshHappyPath(t *testing.T) {
	op := newFakeOperator(t, testClientSecretB64, ScopeUID2, []keyJSON{
		makeKeyJSON(1, 0x11),
		makeKeyJSON(2, 0x22),
	})
	defer op.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, err := New(ctx, Config{
		OperatorURL:        op.URL,
		APIKey:             "test-api-key",
		ClientSecret:       testClientSecretB64,
		IdentityScope:      ScopeUID2,
		HTTPTimeout:        2 * time.Second,
		KeyRefreshInterval: 100 * time.Millisecond,
	})
	require.NoError(t, err)

	// Mint a token against the same keys the fake operator advertises.
	// The client and the server share the same underlying map, so a
	// generator-minted token round-trips through the client's decrypt.
	master := op.keyByID(1)
	site := op.keyByID(2)
	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})

	raw, err := c.Decrypt(t.Context(), token)
	require.NoError(t, err)
	want, _ := base64.StdEncoding.DecodeString(exampleUID2)
	assert.Equal(t, want, raw)
}

// TestNew_InitialRefreshFails asserts New surfaces a non-2xx operator
// response as an error — startup must not silently proceed with an empty
// keyset.
func TestNew_InitialRefreshFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := New(t.Context(), Config{
		OperatorURL:   srv.URL,
		APIKey:        "bad-key",
		ClientSecret:  testClientSecretB64,
		IdentityScope: ScopeUID2,
		HTTPTimeout:   500 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
}

// TestClient_KeyRotation covers picking up a new key mid-run: the
// operator swaps its key set between the initial refresh and a later one,
// and a token minted with the new key must decrypt after the client
// polls again.
func TestClient_KeyRotation(t *testing.T) {
	op := newFakeOperator(t, testClientSecretB64, ScopeUID2, []keyJSON{
		makeKeyJSON(10, 0x11),
		makeKeyJSON(20, 0x22),
	})
	defer op.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, err := New(ctx, Config{
		OperatorURL:        op.URL,
		APIKey:             "test-api-key",
		ClientSecret:       testClientSecretB64,
		IdentityScope:      ScopeUID2,
		HTTPTimeout:        2 * time.Second,
		KeyRefreshInterval: 50 * time.Millisecond,
	})
	require.NoError(t, err)

	// Rotate: introduce brand new keys the client hasn't seen yet.
	op.setKeys([]keyJSON{
		makeKeyJSON(30, 0x33),
		makeKeyJSON(40, 0x44),
	})

	// Force a synchronous refresh — cheaper and less flaky than polling
	// for the background goroutine's schedule.
	require.NoError(t, c.Refresh(t.Context()))

	master := op.keyByID(30)
	site := op.keyByID(40)
	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})
	raw, err := c.Decrypt(t.Context(), token)
	require.NoError(t, err)
	want, _ := base64.StdEncoding.DecodeString(exampleUID2)
	assert.Equal(t, want, raw)
}

// TestClient_ScopeAdvertisedMismatchFailsStartup covers the operator that
// advertises identity_scope=EUID for a UID2-configured client — a hard
// misconfiguration that must fail startup rather than silently accepting
// the wrong keys.
func TestClient_ScopeAdvertisedMismatchFailsStartup(t *testing.T) {
	op := newFakeOperator(t, testClientSecretB64, ScopeEUID, []keyJSON{
		makeKeyJSON(1, 0x11),
		makeKeyJSON(2, 0x22),
	})
	defer op.Close()

	_, err := New(t.Context(), Config{
		OperatorURL:   op.URL,
		APIKey:        "test-api-key",
		ClientSecret:  testClientSecretB64,
		IdentityScope: ScopeUID2, // configured for UID2 but operator says EUID
		HTTPTimeout:   500 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope")
}

// TestClient_ConcurrentDecrypt is a race-detector smoke test that
// hammers Decrypt from many goroutines against a store that is being
// concurrently swapped by refresh. Run with `go test -race`.
func TestClient_ConcurrentDecrypt(t *testing.T) {
	op := newFakeOperator(t, testClientSecretB64, ScopeUID2, []keyJSON{
		makeKeyJSON(1, 0x11),
		makeKeyJSON(2, 0x22),
	})
	defer op.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, err := New(ctx, Config{
		OperatorURL:        op.URL,
		APIKey:             "test-api-key",
		ClientSecret:       testClientSecretB64,
		IdentityScope:      ScopeUID2,
		HTTPTimeout:        2 * time.Second,
		KeyRefreshInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)

	master := op.keyByID(1)
	site := op.keyByID(2)
	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})

	const workers = 16
	const perWorker = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range perWorker {
				_, err := c.Decrypt(t.Context(), token)
				if err != nil {
					t.Errorf("concurrent decrypt failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestNewUID2Config_Defaults confirms the convenience constructor fills
// in the operator URL and scope.
func TestNewUID2Config_Defaults(t *testing.T) {
	cfg := NewUID2Config("api", testClientSecretB64)
	assert.Equal(t, DefaultUID2OperatorURL, cfg.OperatorURL)
	assert.Equal(t, ScopeUID2, cfg.IdentityScope)
	assert.Equal(t, "api", cfg.APIKey)

	cfg = NewEUIDConfig("api", testClientSecretB64)
	assert.Equal(t, DefaultEUIDOperatorURL, cfg.OperatorURL)
	assert.Equal(t, ScopeEUID, cfg.IdentityScope)
}

// TestConfig_ResolveValidates covers the validation surface — a bad
// config must fail at New rather than silently at first refresh.
func TestConfig_ResolveValidates(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		msg  string
	}{
		{
			name: "empty api key",
			cfg:  Config{APIKey: "", ClientSecret: testClientSecretB64, IdentityScope: ScopeUID2},
			msg:  "APIKey is required",
		},
		{
			name: "empty secret",
			cfg:  Config{APIKey: "k", ClientSecret: "", IdentityScope: ScopeUID2},
			msg:  "ClientSecret is required",
		},
		{
			name: "bad base64 secret",
			cfg:  Config{APIKey: "k", ClientSecret: "not-base64!!", IdentityScope: ScopeUID2},
			msg:  "base64-encoded",
		},
		{
			name: "wrong-length secret",
			cfg:  Config{APIKey: "k", ClientSecret: base64.StdEncoding.EncodeToString(make([]byte, 16)), IdentityScope: ScopeUID2},
			msg:  "32 bytes",
		},
		{
			name: "bad operator url",
			cfg:  Config{APIKey: "k", ClientSecret: testClientSecretB64, OperatorURL: "no-scheme.example.com", IdentityScope: ScopeUID2},
			msg:  "http://",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.cfg.resolve()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

// TestDecrypt_RecordsReason covers the observability contract: every
// decrypt call surfaces a reason to the Recorder.
func TestDecrypt_RecordsReason(t *testing.T) {
	rec := &captureRecorder{}
	op := newFakeOperator(t, testClientSecretB64, ScopeUID2, []keyJSON{
		makeKeyJSON(1, 0x11),
		makeKeyJSON(2, 0x22),
	})
	defer op.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, err := New(ctx, Config{
		OperatorURL:        op.URL,
		APIKey:             "test-api-key",
		ClientSecret:       testClientSecretB64,
		IdentityScope:      ScopeUID2,
		HTTPTimeout:        2 * time.Second,
		KeyRefreshInterval: 1 * time.Hour,
		Recorder:           rec,
	})
	require.NoError(t, err)

	master := op.keyByID(1)
	site := op.keyByID(2)
	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})
	_, err = c.Decrypt(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, "success", rec.lastDecryptReason())

	_, err = c.Decrypt(t.Context(), "garbage")
	require.Error(t, err)
	got := rec.lastDecryptReason()
	assert.Contains(t, []string{"invalid", "version_unsupported"}, got)

	// Refresh recording — the initial refresh must have recorded exactly
	// one success outcome (err == nil).
	successes, failures := rec.refreshOutcomes()
	assert.GreaterOrEqual(t, successes, 1, "at least one successful refresh recorded")
	_ = failures
}

// captureRecorder is a thread-safe recording Recorder.
type captureRecorder struct {
	mu             sync.Mutex
	decryptReasons []string
	refreshErrs    []error
}

func (r *captureRecorder) KeyRefresh(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshErrs = append(r.refreshErrs, err)
}

func (r *captureRecorder) TokenDecrypt(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decryptReasons = append(r.decryptReasons, reason)
}

func (r *captureRecorder) lastDecryptReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.decryptReasons) == 0 {
		return ""
	}
	return r.decryptReasons[len(r.decryptReasons)-1]
}

func (r *captureRecorder) refreshOutcomes() (successes, failures int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, err := range r.refreshErrs {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	return
}

// fakeOperator is a UID2-compatible /v2/key/bidstream test server. It
// verifies the request envelope, echoes the client's nonce, and returns
// a keyset described by the JSON records supplied via setKeys.
//
// The server holds the *same key material* the test uses to mint tokens
// (via generateV4Token), so the client and the token minter agree on
// secrets/IDs.
type fakeOperator struct {
	*httptest.Server

	secret []byte
	scope  IdentityScope

	mu   sync.Mutex
	keys map[int64]*key
	json []keyJSON
}

func newFakeOperator(t *testing.T, secretB64 string, scope IdentityScope, initial []keyJSON) *fakeOperator {
	t.Helper()
	secret, err := base64.StdEncoding.DecodeString(secretB64)
	require.NoError(t, err)

	op := &fakeOperator{
		secret: secret,
		scope:  scope,
	}
	op.setKeys(initial)

	op.Server = httptest.NewServer(http.HandlerFunc(op.handle))
	return op
}

func (op *fakeOperator) setKeys(records []keyJSON) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.json = records
	op.keys = make(map[int64]*key, len(records))
	for _, k := range records {
		sec, _ := base64.StdEncoding.DecodeString(k.Secret)
		op.keys[k.ID] = &key{
			id:        k.ID,
			siteID:    k.SiteID,
			created:   time.Unix(k.Created, 0),
			activates: time.Unix(k.Activates, 0),
			expires:   time.Unix(k.Expires, 0),
			secret:    sec,
		}
	}
}

func (op *fakeOperator) keyByID(id int64) *key {
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.keys[id]
}

func (op *fakeOperator) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != keyRefreshPath {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "missing auth", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}
	envelope, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(body)))
	if err != nil {
		http.Error(w, "envelope decode", http.StatusBadRequest)
		return
	}
	if len(envelope) < 1+gcmIVLen+gcmTagLen || envelope[0] != envelopeVersion {
		http.Error(w, "envelope shape", http.StatusBadRequest)
		return
	}
	plain, err := gcmOpen(op.secret, envelope[1:1+gcmIVLen], envelope[1+gcmIVLen:])
	if err != nil {
		http.Error(w, "envelope decrypt", http.StatusBadRequest)
		return
	}
	if len(plain) < unencryptedHeaderLen {
		http.Error(w, "envelope plain", http.StatusBadRequest)
		return
	}
	// Extract the client's nonce so we can echo it back.
	nonce := plain[timestampLen:unencryptedHeaderLen]

	// Build the JSON response body.
	op.mu.Lock()
	scope := "UID2"
	if op.scope == ScopeEUID {
		scope = "EUID"
	}
	respBody := map[string]any{
		"body": map[string]any{
			"identity_scope":                 scope,
			"max_bidstream_lifetime_seconds": 259200,
			"allow_clock_skew_seconds":       1800,
			"keys":                           op.json,
		},
	}
	op.mu.Unlock()

	jsonBody, err := json.Marshal(respBody)
	if err != nil {
		http.Error(w, "marshal response", http.StatusInternalServerError)
		return
	}

	// Encrypt with the same secret and echo the client's nonce.
	responseUnencrypted := make([]byte, 0, unencryptedHeaderLen+len(jsonBody))
	responseUnencrypted = binary.BigEndian.AppendUint64(responseUnencrypted, uint64(time.Now().UnixMilli())) //nolint:gosec
	responseUnencrypted = append(responseUnencrypted, nonce...)
	responseUnencrypted = append(responseUnencrypted, jsonBody...)

	var iv [gcmIVLen]byte
	// deterministic IV isn't required; use zeros for test simplicity.
	ct, err := gcmSeal(op.secret, iv[:], responseUnencrypted)
	if err != nil {
		http.Error(w, "gcm seal", http.StatusInternalServerError)
		return
	}
	wire := append([]byte(nil), iv[:]...)
	wire = append(wire, ct...)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(wire)))
}

// makeKeyJSON builds a keyJSON record with a deterministic secret fill,
// activation window in the past, and expiry far in the future.
func makeKeyJSON(id int64, secretFill byte) keyJSON {
	sec := make([]byte, 32)
	for i := range sec {
		sec[i] = secretFill
	}
	return keyJSON{
		ID:        id,
		KeysetID:  1,
		SiteID:    9000,
		Created:   time.Now().Add(-24 * time.Hour).Unix(),
		Activates: time.Now().Add(-1 * time.Hour).Unix(),
		Expires:   time.Now().Add(24 * time.Hour).Unix(),
		Secret:    base64.StdEncoding.EncodeToString(sec),
	}
}

// Sanity: our fakeOperator is not itself using atomic counters that
// require an import; suppress unused-import complaints when the file
// evolves.
var _ atomic.Bool
