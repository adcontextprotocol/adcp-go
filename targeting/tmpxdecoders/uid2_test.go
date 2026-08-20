package tmpxdecoders

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/uid2client"
)

// stubUID2Client is a hand-rolled fake — no HTTP, no key material. The
// decoders only need Decrypt(ctx, token) to exercise the drop-vs-fail
// mapping.
type stubUID2Client struct {
	raw []byte
	err error
}

func (s stubUID2Client) Decrypt(_ context.Context, _ string) ([]byte, error) {
	return s.raw, s.err
}

func TestUID2_PassesRawBytesThrough(t *testing.T) {
	want := make([]byte, 32)
	for i := range want {
		want[i] = byte(i + 1)
	}
	got, err := UID2{Client: stubUID2Client{raw: want}}.Decode(t.Context(), "any-token")
	require.NoError(t, err)
	assert.Equal(t, want, got,
		"UID2 decoder must return the client's raw bytes verbatim; the sealer's selectEntries enforces the 32-byte contract")
}

// TestUID2_DropSentinels exercises every error the uid2client package
// returns for a per-token failure. Each must be rewritten as
// ErrDropFromSeal so the sealer removes the identity without failing
// the whole request.
func TestUID2_DropSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"invalid", uid2client.ErrInvalidToken},
		{"expired", uid2client.ErrTokenExpired},
		{"key_not_found", uid2client.ErrKeyNotFound},
		{"version_unsupported", uid2client.ErrVersionUnsupported},
		// Scope mismatch lives in the untrusted token prefix (checked
		// before any key lookup or GCM tag verification), so an attacker
		// can flip it trivially — treat it as a routine drop, not an
		// operator page.
		{"scope_mismatch", uid2client.ErrScopeMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UID2{Client: stubUID2Client{err: tc.err}}.Decode(t.Context(), "any-token")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrDropFromSeal,
				"per-token failure %s must map to drop-from-seal", tc.name)
		})
	}
}

// TestUID2_KeysStaleSurfaces confirms ErrKeysStale is NOT swallowed —
// operators need to see it because it means the background refresh is
// broken, not that a single token is bad.
func TestUID2_KeysStaleSurfaces(t *testing.T) {
	_, err := UID2{Client: stubUID2Client{err: uid2client.ErrKeysStale}}.Decode(t.Context(), "any-token")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrDropFromSeal,
		"ErrKeysStale is a config/refresh failure — must not be masked as a drop")
	assert.ErrorIs(t, err, uid2client.ErrKeysStale)
}

// TestUID2_ScopeMismatchDrops confirms ErrScopeMismatch is treated as a
// routine per-token drop rather than a configuration error. The scope
// bit is set from the untrusted token prefix (`(prefix >> 4) & 1`) and
// is checked in decryptToken before any key lookup or GCM tag check —
// an attacker or a misrouted token can flip it trivially, so bubbling it
// up as decoder_error would page operators on adversarial input.
func TestUID2_ScopeMismatchDrops(t *testing.T) {
	_, err := UID2{Client: stubUID2Client{err: uid2client.ErrScopeMismatch}}.Decode(t.Context(), "any-token")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDropFromSeal,
		"ErrScopeMismatch is attacker-reachable via the untrusted token prefix — must drop, not bubble")
}

// TestUID2_EmptyBytesDrops covers a defensive edge — the client could
// theoretically return (nil, nil); if that ever happens we still drop
// rather than crash the sealer with an empty entry.
func TestUID2_EmptyBytesDrops(t *testing.T) {
	_, err := UID2{Client: stubUID2Client{}}.Decode(t.Context(), "tok")
	require.ErrorIs(t, err, ErrDropFromSeal)
}

func TestUID2_NilClientErrors(t *testing.T) {
	_, err := UID2{}.Decode(t.Context(), "tok")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrDropFromSeal,
		"a nil client is a configuration error, not a per-token drop")
}

// TestUID2_UnknownErrorBubbles confirms the error-mapping function
// doesn't accidentally swallow errors it doesn't recognize.
func TestUID2_UnknownErrorBubbles(t *testing.T) {
	boom := errors.New("some other failure")
	_, err := UID2{Client: stubUID2Client{err: boom}}.Decode(t.Context(), "tok")
	require.Error(t, err)
	assert.ErrorContains(t, err, "some other failure")
	assert.NotErrorIs(t, err, ErrDropFromSeal)
}

// TestEUID_UsesSameMapping is a smoke test — the EUID decoder mirrors
// UID2, so we exercise one sentinel and one happy-path to catch a
// regression where the two decoders drift.
func TestEUID_UsesSameMapping(t *testing.T) {
	raw := make([]byte, 32)
	got, err := EUID{Client: stubUID2Client{raw: raw}}.Decode(t.Context(), "tok")
	require.NoError(t, err)
	assert.Equal(t, raw, got)

	_, err = EUID{Client: stubUID2Client{err: uid2client.ErrInvalidToken}}.Decode(t.Context(), "tok")
	require.ErrorIs(t, err, ErrDropFromSeal)
}
