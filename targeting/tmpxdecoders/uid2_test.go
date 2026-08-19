package tmpxdecoders

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubUID2Client returns a fixed raw ID or error. The same fake backs
// both UID2 and EUID decoder tests — the two types share a client
// interface by design.
type stubUID2Client struct {
	raw []byte
	err error
}

func (s stubUID2Client) Decrypt(_ context.Context, _ string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.raw, nil
}

func raw32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestUID2_PassesRawBytesThrough(t *testing.T) {
	want := raw32(0xAB)
	got, err := UID2{Client: stubUID2Client{raw: want}}.Decode(t.Context(), "any-token")
	require.NoError(t, err)
	assert.Equal(t, want, got,
		"decoder must return the operator's raw bytes verbatim — size enforcement is selectEntries' job")
}

func TestUID2_NoMappingDropsIdentity(t *testing.T) {
	_, err := UID2{Client: stubUID2Client{err: ErrUID2NoMapping}}.Decode(t.Context(), "miss")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDropFromSeal, "miss must produce drop sentinel")
}

func TestUID2_EmptyBytesDrops(t *testing.T) {
	_, err := UID2{Client: stubUID2Client{raw: nil}}.Decode(t.Context(), "token")
	require.ErrorIs(t, err, ErrDropFromSeal)
}

func TestUID2_TransportErrorBubbles(t *testing.T) {
	boom := errors.New("connection refused")
	_, err := UID2{Client: stubUID2Client{err: boom}}.Decode(t.Context(), "token")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "connection refused"))
	assert.NotErrorIs(t, err, ErrDropFromSeal)
}

func TestUID2_NilClientErrors(t *testing.T) {
	_, err := UID2{}.Decode(t.Context(), "token")
	require.Error(t, err)
}

func TestEUID_PassesRawBytesThrough(t *testing.T) {
	want := raw32(0xCD)
	got, err := EUID{Client: stubUID2Client{raw: want}}.Decode(t.Context(), "any-token")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestEUID_NoMappingDropsIdentity(t *testing.T) {
	_, err := EUID{Client: stubUID2Client{err: ErrUID2NoMapping}}.Decode(t.Context(), "miss")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDropFromSeal)
}

func TestEUID_NilClientErrors(t *testing.T) {
	_, err := EUID{}.Decode(t.Context(), "token")
	require.Error(t, err)
}
