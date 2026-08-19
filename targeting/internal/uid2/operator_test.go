package uid2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bytes32 returns a repeatable 32-byte fixture — the length UID2/EUID
// raw IDs always take (SHA-256 output). Tests transmit this base64ed
// through the fake adapter and assert on round-trip equality.
func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestNewClient_RequiresURL(t *testing.T) {
	_, err := NewClient(Config{APIKey: "k", ClientSecret: "s"})
	require.Error(t, err)
}

func TestNewClient_RejectsBadScheme(t *testing.T) {
	_, err := NewClient(Config{URL: "ftp://example.com", APIKey: "k", ClientSecret: "s"})
	require.Error(t, err)
}

func TestNewClient_RequiresAPIKey(t *testing.T) {
	_, err := NewClient(Config{URL: "https://example.com", ClientSecret: "s"})
	require.Error(t, err)
}

func TestNewClient_RequiresClientSecret(t *testing.T) {
	_, err := NewClient(Config{URL: "https://example.com", APIKey: "k"})
	require.Error(t, err)
}

func TestDecrypt_HappyPath(t *testing.T) {
	wantRaw := bytes32(0xAB)
	// Case-preserved base64 with mixed-case tokens: UID2 tokens are
	// base64url-alphabet strings; the operator adapter must never
	// touch case on either side, so we assert the token echoes
	// verbatim in the request body.
	const inputToken = "Ag4Xy1z-CaseSensitivePayload==NotLowered" //nolint:gosec // opaque test fixture, not a credential
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		assert.Equal(t, "shhh-secret", r.Header.Get("X-UID2-Client-Secret"))

		var body decryptRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, inputToken, body.Token,
			"token must transit verbatim — the operator adapter is case-sensitive")

		_ = json.NewEncoder(w).Encode(decryptResponse{
			RawID: base64.StdEncoding.EncodeToString(wantRaw),
		})
	}))
	defer srv.Close()

	c, err := NewClient(Config{
		URL:          srv.URL + "/v2/token/decrypt",
		APIKey:       "test-api-key",
		ClientSecret: "shhh-secret",
		HTTPClient:   srv.Client(),
	})
	require.NoError(t, err)

	got, err := c.Decrypt(t.Context(), inputToken)
	require.NoError(t, err)
	assert.Equal(t, wantRaw, got, "raw_id round-trips as its 32-byte canonical binary form")
}

func TestDecrypt_EmptyRawIDMeansNoMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(decryptResponse{RawID: ""})
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestDecrypt_EmptyBodyMeansNoMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestDecrypt_NotFoundMeansNoMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "opted out", http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestDecrypt_GoneMeansNoMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired", http.StatusGone)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.ErrorIs(t, err, ErrNoMapping)
}

func TestDecrypt_UnauthorizedIsDistinctError(t *testing.T) {
	// Bad credentials must surface as a distinct error — never as a
	// silent miss — so operator misconfiguration is visible on the
	// error path rather than masquerading as universal opt-outs.
	// Also assert we don't echo the response body (an adapter that
	// reflects the API key in its 401 body must not leak it back).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "your-api-key-shows-up-here", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "hunter2", ClientSecret: "hunter3", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoMapping, "401 must not be conflated with miss")
	assert.NotContains(t, err.Error(), "your-api-key-shows-up-here", "response body must not appear in the error")
	assert.NotContains(t, err.Error(), "hunter2", "API key must never appear in the error")
	assert.NotContains(t, err.Error(), "hunter3", "client secret must never appear in the error")
}

func TestDecrypt_ForbiddenIsDistinctError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoMapping)
}

func TestDecrypt_5xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoMapping, "5xx must not be conflated with miss")
}

func TestDecrypt_MalformedJSONIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.Error(t, err)
}

func TestDecrypt_MalformedRawIDBase64IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"raw_id":"!!not-base64!!"}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoMapping)
}

func TestDecrypt_WrongSizeRawIDIsError(t *testing.T) {
	// A raw ID that isn't 32 bytes must fail here rather than
	// propagate to the identity-agent's per-type size check — the
	// operator-side error is the actionable one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(decryptResponse{
			RawID: base64.StdEncoding.EncodeToString([]byte("way too short")),
		})
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Decrypt(t.Context(), "any-token")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoMapping)
}

func TestDecrypt_EmptyTokenRejectedClientSide(t *testing.T) {
	c, err := NewClient(Config{URL: "http://example.com", APIKey: "k", ClientSecret: "s"})
	require.NoError(t, err)
	_, err = c.Decrypt(t.Context(), "")
	require.Error(t, err)
}

func TestDecrypt_RespectsContextCancellation(t *testing.T) {
	// Adapter never writes; the test asserts a cancelled context
	// surfaces as an error rather than hanging.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.Decrypt(ctx, "any-token")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled"),
		"expected cancellation error, got %v", err)
}

// TestDecrypt_CaseSensitivityPreservedEndToEnd hardens the case-preserved
// contract: token in and raw_id out both travel verbatim, and the
// decoded bytes match the adapter's underlying byte fixture byte-for-
// byte. A silent ToLower anywhere on the path would break the audience
// keying because raw UID2s are their own SHA-256 output — case cannot
// be reintroduced downstream.
func TestDecrypt_CaseSensitivityPreservedEndToEnd(t *testing.T) {
	fixture := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F, 0x20,
	}
	// Encode into a base64 string that contains BOTH upper- and
	// lower-case characters so a stray ToLower on the wire would
	// silently corrupt the decode.
	encoded := base64.StdEncoding.EncodeToString(fixture)
	assert.NotEqual(t, strings.ToLower(encoded), encoded,
		"test fixture requires mixed-case base64 to be meaningful")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(decryptResponse{RawID: encoded})
	}))
	defer srv.Close()

	c, err := NewClient(Config{URL: srv.URL, APIKey: "k", ClientSecret: "s", HTTPClient: srv.Client()})
	require.NoError(t, err)

	got, err := c.Decrypt(t.Context(), "MixedCaseTokenPayload==")
	require.NoError(t, err)
	assert.Equal(t, fixture, got)
}
