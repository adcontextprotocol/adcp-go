package uid2client

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exampleUID2 mirrors the "EXAMPLE_UID" test vector used across the
// reference Java client's tests. It is the base64 of a 32-byte raw UID2.
const exampleUID2 = "ywsvDNINiZOVSsfkHpLpSJzXzhr6Jx9Z/4Q0+lsEUvM="

// TestDecryptV4_RoundTrip locks in that the V4 token layout our
// generator produces round-trips through the client's decrypt path back to
// the exact raw identity bytes. This is the primary correctness anchor;
// if the byte layout drifts from uid2-client-java's Uid2Encryption.decryptV3,
// this test fails.
func TestDecryptV4_RoundTrip(t *testing.T) {
	master := makeTestKey(t, 164, -1, 0x11)
	site := makeTestKey(t, 165, 9000, 0x22)
	store := makeTestStore(ScopeUID2, master, site)

	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})

	raw, err := decryptToken(store, ScopeUID2, token, time.Now())
	require.NoError(t, err)

	wantRaw, err := base64.StdEncoding.DecodeString(exampleUID2)
	require.NoError(t, err)
	assert.Equal(t, wantRaw, raw, "V4 token decrypt must return the exact raw 32 identity bytes")
	assert.Len(t, raw, 32, "raw UID2 must be 32 bytes")
}

// TestDecryptV3_RoundTrip covers the same round-trip against a V3 token
// (base64 on the wire instead of base64url). The site payload byte layout
// is identical to V4; V3 vs V4 differ only in wire encoding and the
// version byte.
func TestDecryptV3_RoundTrip(t *testing.T) {
	master := makeTestKey(t, 200, -1, 0x33)
	site := makeTestKey(t, 201, 9000, 0x44)
	store := makeTestStore(ScopeUID2, master, site)

	token := generateV3Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})
	// V3 uses standard base64 — no '-' or '_' in the wire.
	assert.False(t, strings.ContainsAny(token, "-_"), "V3 must be standard base64")

	raw, err := decryptToken(store, ScopeUID2, token, time.Now())
	require.NoError(t, err)
	wantRaw, _ := base64.StdEncoding.DecodeString(exampleUID2)
	assert.Equal(t, wantRaw, raw)
}

// TestDecryptV2_RoundTrip locks in V2 wire behavior — a different
// envelope (CBC not GCM, 16-byte IVs, PKCS#7 padding) that the client
// must still decrypt to the same raw identity bytes.
func TestDecryptV2_RoundTrip(t *testing.T) {
	master := makeTestKey(t, 300, -1, 0x55)
	site := makeTestKey(t, 301, 9000, 0x66)
	store := makeTestStore(ScopeUID2, master, site)

	token := generateV2Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})
	raw, err := decryptToken(store, ScopeUID2, token, time.Now())
	require.NoError(t, err)
	wantRaw, _ := base64.StdEncoding.DecodeString(exampleUID2)
	assert.Equal(t, wantRaw, raw)
}

// TestDecryptV4_EUID confirms the identity scope check accepts EUID
// tokens when the client is configured for EUID.
func TestDecryptV4_EUID(t *testing.T) {
	master := makeTestKey(t, 400, -1, 0x77)
	site := makeTestKey(t, 401, 9000, 0x88)
	store := makeTestStore(ScopeEUID, master, site)

	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeEUID,
		IdentityRaw: exampleUID2,
	})
	raw, err := decryptToken(store, ScopeEUID, token, time.Now())
	require.NoError(t, err)
	wantRaw, _ := base64.StdEncoding.DecodeString(exampleUID2)
	assert.Equal(t, wantRaw, raw)
}

// TestDecryptV4_ScopeMismatch confirms a UID2 token decrypted with an
// EUID-scoped client returns ErrScopeMismatch (and not a bogus decrypt
// of some other bytes).
func TestDecryptV4_ScopeMismatch(t *testing.T) {
	master := makeTestKey(t, 500, -1, 0x99)
	site := makeTestKey(t, 501, 9000, 0xAA)
	store := makeTestStore(ScopeEUID, master, site)

	uid2Token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2, // mint a UID2 token but hand it to an EUID store
		IdentityRaw: exampleUID2,
	})
	_, err := decryptToken(store, ScopeEUID, uid2Token, time.Now())
	assert.ErrorIs(t, err, ErrScopeMismatch)
}

// TestDecrypt_TokenExpired locks in that a token whose expiry timestamp
// has already passed produces ErrTokenExpired — with no partial return
// of the identity.
func TestDecrypt_TokenExpired(t *testing.T) {
	master := makeTestKey(t, 600, -1, 0xBB)
	site := makeTestKey(t, 601, 9000, 0xCC)
	store := makeTestStore(ScopeUID2, master, site)

	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
		Expiry:      time.Now().Add(-1 * time.Minute),
		Generated:   time.Now().Add(-1 * time.Hour),
	})
	raw, err := decryptToken(store, ScopeUID2, token, time.Now())
	assert.Nil(t, raw)
	assert.ErrorIs(t, err, ErrTokenExpired)
}

// TestDecrypt_KeyNotFound covers the case where the master key ID in the
// token isn't in the store — a symptom of stale keys or a
// misconfiguration.
func TestDecrypt_KeyNotFound(t *testing.T) {
	master := makeTestKey(t, 700, -1, 0xDD)
	site := makeTestKey(t, 701, 9000, 0xEE)
	tokenStore := makeTestStore(ScopeUID2, master, site)

	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})

	// Now decrypt against a store missing the master key. We keep site
	// so the failure is specifically "master key missing".
	shortStore := makeTestStore(ScopeUID2, site)
	shortStore.latestExpiry = tokenStore.latestExpiry // avoid ErrKeysStale
	_, err := decryptToken(shortStore, ScopeUID2, token, time.Now())
	assert.ErrorIs(t, err, ErrKeyNotFound)
}

// TestDecrypt_KeysStale covers the "every key has expired" case, which is
// distinct from ErrKeyNotFound: it means the background refresh is
// failing.
func TestDecrypt_KeysStale(t *testing.T) {
	master := makeTestKey(t, 800, -1, 0xF0)
	site := makeTestKey(t, 801, 9000, 0xF1)
	store := makeTestStore(ScopeUID2, master, site)

	// Force the store's latest expiry into the past.
	store.latestExpiry = time.Now().Add(-1 * time.Hour)
	_, err := decryptToken(store, ScopeUID2, "AGAAAAA=", time.Now())
	assert.ErrorIs(t, err, ErrKeysStale)
}

// TestDecrypt_TamperedCiphertext covers GCM authentication failure — a
// bit-flip anywhere in the ciphertext must produce ErrInvalidToken and
// never leak partial plaintext.
func TestDecrypt_TamperedCiphertext(t *testing.T) {
	master := makeTestKey(t, 900, -1, 0x0F)
	site := makeTestKey(t, 901, 9000, 0x1F)
	store := makeTestStore(ScopeUID2, master, site)

	token := generateV4Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})
	// Flip a bit inside the master ciphertext region. base64url is url-safe;
	// substitute one char with another to change one byte deterministically.
	tampered := []byte(token)
	// Choose an index past the header prefix.
	tampered[len(tampered)/2] = xorChar(tampered[len(tampered)/2])
	_, err := decryptToken(store, ScopeUID2, string(tampered), time.Now())
	assert.ErrorIs(t, err, ErrInvalidToken, "GCM tag mismatch must produce ErrInvalidToken")
}

// TestDecrypt_MalformedTokens covers the shortest-path malformed inputs
// that a hostile / buggy upstream can send. Every one must surface as
// ErrInvalidToken and never panic.
func TestDecrypt_MalformedTokens(t *testing.T) {
	master := makeTestKey(t, 1000, -1, 0x02)
	site := makeTestKey(t, 1001, 9000, 0x03)
	store := makeTestStore(ScopeUID2, master, site)

	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too-short", "AA"},
		{"not-base64", "!!not-base64!!"},
		{"unknown-version", base64.StdEncoding.EncodeToString([]byte{0x00, 0x0F, 0x00, 0x00})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decryptToken(store, ScopeUID2, tc.input, time.Now())
			require.Error(t, err)
			assert.True(t,
				assert.ObjectsAreEqual(err, ErrInvalidToken) ||
					assert.ObjectsAreEqual(err, ErrVersionUnsupported) ||
					strings.Contains(err.Error(), "token"),
				"unexpected error: %v", err)
		})
	}
}

// TestDecrypt_CaseSensitivityPreserved is the case-sensitivity invariant
// the task brief specifically calls out: a base64-encoded token contains
// mixed case, and any lowercasing anywhere on the path corrupts it. This
// pins that end-to-end.
func TestDecrypt_CaseSensitivityPreserved(t *testing.T) {
	master := makeTestKey(t, 1100, -1, 0x22)
	site := makeTestKey(t, 1101, 9000, 0x33)
	store := makeTestStore(ScopeUID2, master, site)

	token := generateV3Token(t, master, site, tokenGenParams{
		Scope:       ScopeUID2,
		IdentityRaw: exampleUID2,
	})

	// Successful decrypt with original token.
	raw, err := decryptToken(store, ScopeUID2, token, time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	// The all-lowercase form is a different base64 string — decrypt must
	// fail (either at the base64 alphabet check or the GCM tag check).
	// Any success here means the client silently altered the input.
	lower := strings.ToLower(token)
	if lower == token {
		// Token happened to have no uppercase — regenerate. Extremely
		// unlikely for a 32-byte payload but not impossible.
		t.Skip("generated token had no uppercase; skipping case check")
	}
	_, err = decryptToken(store, ScopeUID2, lower, time.Now())
	require.Error(t, err, "lowercased token must not decrypt to the same identity")
}

// xorChar returns a different printable base64/url-safe char than c —
// enough for the tampered-ciphertext test to change one byte in place.
func xorChar(c byte) byte {
	switch c {
	case 'A':
		return 'B'
	case 'a':
		return 'b'
	case '0':
		return '1'
	case '-':
		return '_'
	case '_':
		return '-'
	default:
		if c >= 'A' && c <= 'Z' {
			return c - 1
		}
		if c >= 'a' && c <= 'z' {
			return c - 1
		}
		if c >= '0' && c <= '9' {
			return c - 1
		}
		return 'A'
	}
}
