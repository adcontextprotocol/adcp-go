package tmpxdecoders

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func TestCanonicalIsLowercaseHex(t *testing.T) {
	cases := map[string][]byte{
		"":                                {},
		"00":                              {0x00},
		"ff":                              {0xff},
		"550e8400e29b41d4a716446655440000": {
			0x55, 0x0e, 0x84, 0x00, 0xe2, 0x9b, 0x41, 0xd4,
			0xa7, 0x16, 0x44, 0x66, 0x55, 0x44, 0x00, 0x00,
		},
	}
	for want, in := range cases {
		got := Canonical(in)
		assert.Equal(t, want, got)
		assert.Equal(t, strings.ToLower(got), got, "Canonical output must be lowercase — downstream stores key on this exact form")
	}
}

type stubDecoder struct {
	out []byte
	err error
}

func (s stubDecoder) Decode(context.Context, string) ([]byte, error) {
	return s.out, s.err
}

func TestDecodeHexReturnsCanonicalFormOfDecodedBytes(t *testing.T) {
	registry := map[tmproto.UIDType]Decoder{
		tmproto.UIDTypeMAID: stubDecoder{out: []byte{0xab, 0xcd, 0xef}},
	}
	got, err := DecodeHex(context.Background(), registry, tmproto.UIDTypeMAID, "any-input")
	require.NoError(t, err)
	assert.Equal(t, "abcdef", got)
}

func TestDecodeHexSilentlyDropsUnregisteredTypes(t *testing.T) {
	got, err := DecodeHex(context.Background(), map[tmproto.UIDType]Decoder{}, tmproto.UIDTypeMAID, "any")
	require.NoError(t, err)
	assert.Equal(t, "", got, "unregistered uidType returns the empty string, mirroring the silent-drop semantics of the registry")
}

func TestDecodeHexPropagatesDropFromSeal(t *testing.T) {
	registry := map[tmproto.UIDType]Decoder{
		tmproto.UIDTypeRampID: stubDecoder{err: ErrDropFromSeal},
	}
	got, err := DecodeHex(context.Background(), registry, tmproto.UIDTypeRampID, "env")
	assert.Equal(t, "", got)
	require.True(t, errors.Is(err, ErrDropFromSeal), "ErrDropFromSeal must surface so callers can apply the drop policy")
}

func TestDecodeHexPropagatesOtherErrors(t *testing.T) {
	sentinel := errors.New("decoder boom")
	registry := map[tmproto.UIDType]Decoder{
		tmproto.UIDTypeMAID: stubDecoder{err: sentinel},
	}
	_, err := DecodeHex(context.Background(), registry, tmproto.UIDTypeMAID, "bad-input")
	require.True(t, errors.Is(err, sentinel))
}
