package uid2client

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseKeyRefreshResponse_Success covers the shape of a real
// /v2/key/bidstream response body. If the JSON field names drift from
// what the operator actually publishes, this test catches it.
func TestParseKeyRefreshResponse_Success(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"body": map[string]any{
			"identity_scope":                 "UID2",
			"max_bidstream_lifetime_seconds": 259200,
			"allow_clock_skew_seconds":       1800,
			"keys": []any{
				map[string]any{
					"id":        int64(164),
					"keyset_id": 1,
					"created":   int64(1_700_000_000),
					"activates": int64(1_700_000_001),
					"expires":   int64(1_900_000_000),
					"secret":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
				},
			},
		},
	})

	store, err := parseKeyRefreshResponse(body, ScopeUID2)
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.Equal(t, ScopeUID2, store.scope)
	require.Len(t, store.keys, 1)

	k, ok := store.keys[164]
	require.True(t, ok)
	assert.Equal(t, int64(164), k.id)
	assert.Equal(t, 1, k.keysetID)
	assert.Len(t, k.secret, 32)
	assert.True(t, store.isValid(time.Unix(1_800_000_000, 0)), "store must be valid before latestExpiry")
}

// TestParseKeyRefreshResponse_EUIDScope covers the EUID-flavored response.
func TestParseKeyRefreshResponse_EUIDScope(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"body": map[string]any{
			"identity_scope": "EUID",
			"keys":           []any{oneValidKeyJSON(t, 42)},
		},
	})
	store, err := parseKeyRefreshResponse(body, ScopeUID2)
	require.NoError(t, err)
	assert.Equal(t, ScopeEUID, store.scope, "identity_scope in response overrides configured scope for detection")
}

// TestParseKeyRefreshResponse_ScopeMissing_UsesConfigured covers older
// operators that omit the identity_scope field.
func TestParseKeyRefreshResponse_ScopeMissing_UsesConfigured(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"body": map[string]any{
			"keys": []any{oneValidKeyJSON(t, 42)},
		},
	})
	store, err := parseKeyRefreshResponse(body, ScopeEUID)
	require.NoError(t, err)
	assert.Equal(t, ScopeEUID, store.scope)
}

// TestParseKeyRefreshResponse_NoKeys is a defensive check — an operator
// error that returned an empty keyset would silently disable the client
// otherwise.
func TestParseKeyRefreshResponse_NoKeys(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"body": map[string]any{
			"keys": []any{},
		},
	})
	_, err := parseKeyRefreshResponse(body, ScopeUID2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no keys")
}

// TestParseKeyRefreshResponse_BadKeyLength catches an operator that
// returned a key of the wrong size — would otherwise blow up deep inside
// AES setup at token-decrypt time.
func TestParseKeyRefreshResponse_BadKeyLength(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"body": map[string]any{
			"keys": []any{
				map[string]any{
					"id":        int64(1),
					"created":   int64(1),
					"activates": int64(1),
					"expires":   int64(1_900_000_000),
					"secret":    base64.StdEncoding.EncodeToString(make([]byte, 16)), // wrong length
				},
			},
		},
	})
	_, err := parseKeyRefreshResponse(body, ScopeUID2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want 32")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func oneValidKeyJSON(t *testing.T, id int64) map[string]any {
	t.Helper()
	return map[string]any{
		"id":        id,
		"keyset_id": 1,
		"created":   int64(1_700_000_000),
		"activates": int64(1_700_000_001),
		"expires":   int64(1_900_000_000),
		"secret":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
}
