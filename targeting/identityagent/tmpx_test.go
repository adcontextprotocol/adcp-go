package identityagent

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/tmpxdecoders"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

const testNullifier = "0x" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// ChunkEntries pairs the sealed token with the provider's registered slot
// IDs. Single-slot deployments emit exactly one entry carrying the whole
// token.
func TestChunkEntries_SingleSlotEmitsWholeToken(t *testing.T) {
	s := &TMPXSealer{slotIDs: []string{"primary"}}
	entries := s.ChunkEntries("k1.token")
	require.Len(t, entries, 1)
	assert.Equal(t, "primary", entries[0].SlotID)
	assert.Equal(t, "k1.token", entries[0].Value)
}

// ChunkEntries returns nil on three independently-disabling conditions:
// nil receiver, empty token, no registered slot IDs. Without either side
// the pair is meaningless and no chunks are emitted.
func TestChunkEntries_NotEmittedWhenDisabled(t *testing.T) {
	var nilSealer *TMPXSealer
	assert.Nil(t, nilSealer.ChunkEntries("k1.token"), "nil sealer must not produce entries")

	noSlots := &TMPXSealer{}
	assert.Nil(t, noSlots.ChunkEntries("k1.token"), "sealer without registered slot IDs must not produce entries")

	noToken := &TMPXSealer{slotIDs: []string{"primary"}}
	assert.Nil(t, noToken.ChunkEntries(""), "empty token must not produce entries")
}

// ChunkEntries with two slots but a token that fits in one slot emits only
// the first slot; the empty trailing slot is dropped rather than emitted
// with an empty value. This matches the AdCP wire contract: consumers
// reassemble by concatenating present entries in order, and a trailing
// empty entry would confuse "how many chunks did the sealer intend" for a
// receiver that also supports N>2 in a later version.
func TestChunkEntries_TwoSlots_TokenFitsInFirst(t *testing.T) {
	s := &TMPXSealer{slotIDs: []string{"PIN_TMPX_1", "PIN_TMPX_2"}}
	token := strings.Repeat("a", 100) // < 255 bytes
	entries := s.ChunkEntries(token)
	require.Len(t, entries, 1, "second slot MUST NOT be emitted when the token fits in the first")
	assert.Equal(t, "PIN_TMPX_1", entries[0].SlotID)
	assert.Equal(t, token, entries[0].Value)
}

// ChunkEntries with two slots and a token that straddles both slots
// chunks at exactly TmpxMaxWireBytes (255) — the GAM %%PATTERN_MACRO%%
// substitution limit. Reassembly is byte-concatenation in slot order,
// so the boundary must be deterministic.
func TestChunkEntries_TwoSlots_TokenStraddlesBothSlots(t *testing.T) {
	s := &TMPXSealer{slotIDs: []string{"PIN_TMPX_1", "PIN_TMPX_2"}}
	// Build a 300-byte token from distinguishable halves so we can pin the
	// chunk boundary at 255.
	first := strings.Repeat("a", tmproto.TmpxMaxWireBytes)
	rest := strings.Repeat("b", 45)
	token := first + rest
	entries := s.ChunkEntries(token)
	require.Len(t, entries, 2, "both slots must be emitted when the token exceeds one slot")
	assert.Equal(t, "PIN_TMPX_1", entries[0].SlotID)
	assert.Equal(t, "PIN_TMPX_2", entries[1].SlotID)
	require.Len(t, entries[0].Value, tmproto.TmpxMaxWireBytes, "first chunk MUST fill exactly one slot")
	assert.Equal(t, first, entries[0].Value)
	assert.Equal(t, rest, entries[1].Value)
	// Reassembly is byte-concatenation in slot order.
	assert.Equal(t, token, entries[0].Value+entries[1].Value)
}

// ChunkEntries fails closed on an over-budget token: a token longer than
// len(slotIDs) * TmpxMaxWireBytes cannot be reassembled by the receiver
// (the trailing bytes have no slot), so returning truncated chunks would
// silently corrupt downstream reassembly. The sealer's selectEntries pass
// keeps produced tokens within budget, so this is a defensive floor against
// a future change that raises the seal budget without bumping the slot count.
func TestChunkEntries_FailsClosedOnOverBudgetToken(t *testing.T) {
	s := &TMPXSealer{slotIDs: []string{"PIN_TMPX_1", "PIN_TMPX_2"}}
	// A token that would need three chunks; the sealer should have refused
	// to produce it. ChunkEntries surfaces this as "no TMPX" — an identity
	// drop — rather than truncating.
	oversize := strings.Repeat("a", 3*tmproto.TmpxMaxWireBytes)
	assert.Nil(t, s.ChunkEntries(oversize),
		"over-budget token MUST return nil so the response falls back to no-TMPX rather than truncated chunks")
}

func TestVerifiedIdentityEntries_EncodesNullifier(t *testing.T) {
	cfg := &TMPXSealer{}
	entries := cfg.verifiedIdentityEntries(t.Context(), []targeting.VerifiedIdentity{
		{Nullifier: testNullifier, RelyingPartyID: "rp.example"},
	})
	require.Len(t, entries, 1)
	assert.Equal(t, tmproto.UIDTypeWorldIDNullifier, entries[0].UIDType)
	size, _ := tmproto.TmpxTokenSize(tmproto.TmpxTypeWorldIDNullifier)
	assert.Len(t, entries[0].Bytes, size, "verified nullifier must encode to the registry token width")
}

func TestVerifiedIdentityEntries_SkipsEmptyAndMalformed(t *testing.T) {
	rec := newTestRecorder()
	cfg := &TMPXSealer{recorder: rec}
	entries := cfg.verifiedIdentityEntries(t.Context(), []targeting.VerifiedIdentity{
		{Nullifier: ""}, // skipped, no counter
		{Nullifier: "0xnothex", RelyingPartyID: "rp.example"},    // malformed → decoder_error drop
		{Nullifier: testNullifier, RelyingPartyID: "rp.example"}, // survives
	})
	require.Len(t, entries, 1, "only the well-formed nullifier survives")
	assert.Equal(t, 1, rec.dropCount(TmpxDropDecoderError, string(tmproto.UIDTypeWorldIDNullifier)),
		"a malformed nullifier records a decoder_error drop and does not fail the batch")
}

func TestVerifiedIdentityEntries_DropsMissingRelyingParty(t *testing.T) {
	rec := newTestRecorder()
	cfg := &TMPXSealer{recorder: rec}
	entries := cfg.verifiedIdentityEntries(t.Context(), []targeting.VerifiedIdentity{
		{Nullifier: testNullifier}, // no relying party → cannot form an rp-scoped token
	})
	assert.Empty(t, entries, "a verified nullifier without a relying party cannot be sealed")
	assert.Equal(t, 1, rec.dropCount(TmpxDropDecoderError, string(tmproto.UIDTypeWorldIDNullifier)))
}

// TestVerifiedNullifierSealsWhileInboundAssertedDropped is the verify-before-trust
// end-to-end check: a verifier-derived nullifier reaches the wire, while a
// sender-asserted world_id_nullifier supplied on req.Identities is dropped at
// decode (no inbound decoder) and never sealed.
func TestVerifiedNullifierSealsWhileInboundAssertedDropped(t *testing.T) {
	cfg := &TMPXSealer{
		country:  "US",
		encStore: newFakeResolver(t, "k1"),
		decoders: defaultTestDecoders(t),
	}

	// Sender-asserted world_id_nullifier on the inbound identities: dropped.
	inbound := cfg.Decode(t.Context(), []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeWorldIDNullifier, UserToken: testNullifier},
	})
	require.Len(t, inbound, 1)
	assert.Empty(t, inbound[0].Bytes, "inbound asserted world_id_nullifier has no decoder and must drop")

	inboundEntries, err := cfg.selectEntries(inbound)
	require.NoError(t, err)
	assert.Empty(t, inboundEntries, "asserted nullifier must not reach the wire")

	// Verifier-derived nullifier: packs into a wire entry.
	verified := cfg.verifiedIdentityEntries(t.Context(), []targeting.VerifiedIdentity{
		{Nullifier: testNullifier, RelyingPartyID: "rp.example"},
	})
	entries, err := cfg.selectEntries(append(inbound, verified...))
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the verified nullifier is sealed")
	assert.Equal(t, tmproto.TmpxTypeWorldIDNullifier, entries[0].TypeID)

	wire, err := cfg.SealDecoded(t.Context(), append(inbound, verified...))
	require.NoError(t, err)
	assert.NotEmpty(t, wire, "verified nullifier must produce a TMPX wire")
}

// fakeRecipientResolver returns a fixed recipient. Used to exercise
// (*TMPXSealer).Seal without spinning up an httptest JWKS server.
type fakeRecipientResolver struct {
	recipient tmproto.TmpxRecipient
	ok        bool
}

func (f *fakeRecipientResolver) CurrentEncryptionRecipient() (tmproto.TmpxRecipient, bool) {
	return f.recipient, f.ok
}

func newFakeResolver(t *testing.T, kid string) *fakeRecipientResolver {
	t.Helper()
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &fakeRecipientResolver{
		recipient: tmproto.TmpxRecipient{Kid: kid, PublicKey: sk.PublicKey()},
		ok:        true,
	}
}

func TestNewTMPXSealerDisabled(t *testing.T) {
	sealer, err := NewTMPXSealer(t.Context(), TMPXConfig{}, nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, sealer)
}

func TestNewTMPXSealerPartialFails(t *testing.T) {
	cases := []TMPXConfig{
		{EncryptJWKSURL: "https://example.com/jwks.json"},
		{Country: "US"},
	}
	for _, c := range cases {
		_, err := NewTMPXSealer(t.Context(), c, nil, nil, nil)
		assert.Error(t, err, "partial config %+v should fail", c)
	}
}

func TestTMPXSealerFromJWKSServer(t *testing.T) {
	encKey := mustEncKeyJSON(t, "kid-abc")
	body, err := json.Marshal(map[string]any{"keys": []map[string]any{encKey}})
	require.NoError(t, err)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	// JWKSStore mandates https://, which httptest.NewTLSServer provides;
	// AllowInsecureScheme isn't exposed via NewTMPXSealer, so we bypass
	// it by constructing TMPXSealer directly with a custom-clienteted
	// JWKSStore. This still exercises the resolver interface.
	cfg := &TMPXSealer{
		country:  "US",
		encStore: testJWKSStoreFor(t, srv),
		priority: []tmproto.UIDType{tmproto.UIDTypeUID2},
	}
	rcp, ok := cfg.encStore.CurrentEncryptionRecipient()
	require.True(t, ok)
	assert.Equal(t, "kid-abc", rcp.Kid)
}

func TestSeal_ProducesParseableWireFormat(t *testing.T) {
	// Structural smoke test: the wire is `kid.b64url(payload)`, the kid
	// matches the configured recipient, and the payload is at least the
	// HPKE envelope (32-byte enc + 16-byte tag) plus header. Byte-level
	// roundtrip of the inner plaintext is covered at the selectEntries
	// layer (see TestSelectEntries_*DecoderProducesExpectedBytes), since
	// HPKE Open is package-private to tmproto.
	resolver := newFakeResolver(t, "k1")
	cfg := &TMPXSealer{
		country:  "US",
		encStore: resolver,
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeOther, UserToken: "ignored"},
	}
	wire, err := cfg.Seal(t.Context(), ids)
	require.NoError(t, err)
	kid, payload, ok := strings.Cut(wire, ".")
	require.True(t, ok, "wire format: %q", wire)
	assert.Equal(t, "k1", kid)
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	require.NoError(t, err)
	assert.Greater(t, len(raw), 32+16, "payload suspiciously short")
}

func TestSealEmptyWhenNoMappableIdentities(t *testing.T) {
	cfg := &TMPXSealer{
		country:  "US",
		encStore: newFakeResolver(t, "k1"),
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{{UIDType: tmproto.UIDTypeOther, UserToken: "x"}}
	wire, err := cfg.Seal(t.Context(), ids)
	require.NoError(t, err)
	assert.Empty(t, wire, "expected empty wire when no mappable identities")
}

func TestSealErrorsWhenJWKSPublishesNoEncryptionKey(t *testing.T) {
	cfg := &TMPXSealer{
		country:  "US",
		encStore: &fakeRecipientResolver{ok: false},
		decoders: defaultTestDecoders(t),
	}
	_, err := cfg.Seal(t.Context(), []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	})
	assert.Error(t, err, "expected error when JWKS has no encryption key")
}

func TestSeal_DecoderErrorDropsIdentityNotWholeToken(t *testing.T) {
	rec := newTestRecorder()
	cfg := &TMPXSealer{
		country:  "US",
		encStore: newFakeResolver(t, "k1"),
		decoders: defaultTestDecoders(t),
		recorder: rec,
	}
	// One identity is malformed (HashedEmail with non-hex input); the
	// other is valid. The malformed one must drop with a counter; the
	// valid one must still ship.
	wire, err := cfg.Seal(t.Context(), []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: fixtureToken("malformed-hashed-email")},
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	})
	require.NoError(t, err, "one bad identity must not fail the whole token")
	require.NotEmpty(t, wire, "valid identity must still produce a TMPX wire")
	assert.Equal(t, 1, rec.dropCount(TmpxDropDecoderError, string(tmproto.UIDTypeHashedEmail)),
		"malformed hashed_email must record a decoder_error drop")
}

func TestSelectEntries_HashedEmailDecoderProducesExpectedBytes(t *testing.T) {
	// HashedEmail decoder must hex-decode the 64-char SHA-256 string to
	// its 32 raw bytes.
	cfg := &TMPXSealer{
		priority: []tmproto.UIDType{tmproto.UIDTypeHashedEmail},
		decoders: defaultTestDecoders(t),
	}
	const userToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	want := []byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: userToken},
	}
	entries, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, tmproto.TmpxTypeHashedEmail, entries[0].TypeID)
	assert.Equal(t, want, entries[0].Token,
		"HashedEmail decoder must yield the raw 32 bytes of the SHA-256 input")
}

func TestDecode_PopulatesBytesOnlyForRegisteredDecoders(t *testing.T) {
	cfg := &TMPXSealer{decoders: defaultTestDecoders(t)}
	decoded := cfg.Decode(t.Context(), []tmproto.IdentityToken{
		// MAID has a decoder.
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		// HashedEmail has a decoder.
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		// UID2 has no registered decoder — dropped at decode time.
		{UIDType: tmproto.UIDTypeUID2, UserToken: "anything"},
		// RampID via fake LR client.
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		// UIDTypeOther has no TMPX mapping — dropped.
		{UIDType: tmproto.UIDTypeOther, UserToken: "x"},
	})
	require.Len(t, decoded, 5)
	assert.NotEmpty(t, decoded[0].Bytes, "MAID must be decoded")
	assert.NotEmpty(t, decoded[1].Bytes, "HashedEmail must be decoded")
	assert.Empty(t, decoded[2].Bytes, "UID2 has no decoder and must be dropped")
	assert.NotEmpty(t, decoded[3].Bytes, "RampID with LR enabled must be decoded")
	assert.Empty(t, decoded[4].Bytes, "unmapped type must have no bytes")
}

// TestNoDecoder_DropsFromBothWireAndAudience locks in the contract that a
// UID type without a registered decoder is dropped from BOTH the TMPX wire
// and the audience/fcap shadow request — driven by the same Decode pass
// to prove the two consumers can't disagree.
func TestNoDecoder_DropsFromBothWireAndAudience(t *testing.T) {
	cfg := &TMPXSealer{decoders: defaultTestDecoders(t)}
	decoded := cfg.Decode(t.Context(), []tmproto.IdentityToken{
		// UID2 has no registered decoder; must be dropped from both paths.
		{UIDType: tmproto.UIDTypeUID2, UserToken: "any-uid2"},
		// MAID has a decoder; must survive on both paths.
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	})

	shadow := audienceEligibleIdentities(decoded)
	require.Len(t, shadow, 1, "shadow must contain only MAID")
	assert.Equal(t, tmproto.UIDTypeMAID, shadow[0].UIDType)

	entries, err := cfg.selectEntries(decoded)
	require.NoError(t, err)
	require.Len(t, entries, 1, "wire must contain only MAID")
	assert.Equal(t, tmproto.TmpxTypeMAID, entries[0].TypeID)
}

func TestAudienceEligibleIdentities_FiltersDropped(t *testing.T) {
	decoded := []DecodedIdentity{
		{UIDType: tmproto.UIDTypeMAID, Bytes: []byte{0x01, 0x02, 0x03}},
		{UIDType: tmproto.UIDTypeUID2, Bytes: nil},   // dropped at decode (no decoder)
		{UIDType: tmproto.UIDTypeRampID, Bytes: nil}, // dropped (LR miss)
		{UIDType: tmproto.UIDTypeHashedEmail, Bytes: []byte{0xff, 0xee}},
	}
	got := audienceEligibleIdentities(decoded)
	require.Len(t, got, 2, "must keep only non-empty entries")
	assert.Equal(t, tmproto.UIDTypeMAID, got[0].UIDType)
	assert.Equal(t, "010203", got[0].UserToken, "MAID UserToken is the canonical lowercase-hex form")
	assert.Equal(t, tmproto.UIDTypeHashedEmail, got[1].UIDType)
	assert.Equal(t, "ffee", got[1].UserToken, "HashedEmail UserToken is the canonical lowercase-hex form")
}

func TestServiceIdentities_PreservesUndecodedAttestation(t *testing.T) {
	att := &tmproto.Attestation{
		Scheme: "world_id_v4",
		Proof:  map[string]any{"responses": []any{}},
	}
	inbound := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: "maid-raw"},
		// Not inbound-decodable, carries the attestation the verified-identity
		// stage needs.
		{UIDType: tmproto.UIDTypeWorldIDNullifier, UserToken: testNullifier, Attestation: att},
	}
	decoded := []DecodedIdentity{
		{UIDType: tmproto.UIDTypeMAID, Bytes: []byte{0x01, 0x02, 0x03}},
		{UIDType: tmproto.UIDTypeWorldIDNullifier, Bytes: nil}, // no decoder → dropped
	}

	got := serviceIdentities(inbound, decoded)
	require.Len(t, got, 2, "decoded MAID plus the undecoded World attestation carrier")

	// Decoded MAID is represented by its canonical token, no attestation.
	assert.Equal(t, tmproto.UIDTypeMAID, got[0].UIDType)
	assert.Equal(t, "010203", got[0].UserToken)
	assert.Nil(t, got[0].Attestation)

	// World token survives with its attestation and raw nullifier intact so the
	// verified-identity stage can verify it.
	assert.Equal(t, tmproto.UIDTypeWorldIDNullifier, got[1].UIDType)
	assert.Equal(t, testNullifier, got[1].UserToken)
	require.NotNil(t, got[1].Attestation)
	assert.Equal(t, "world_id_v4", got[1].Attestation.Scheme)
}

func TestServiceIdentities_NoAttestationDropsUndecoded(t *testing.T) {
	// An undecodable identity with NO attestation is not re-added — only the
	// canonical (decoded) set represents it, and here it decoded to nothing.
	inbound := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeUID2, UserToken: "uid2-raw"},
	}
	decoded := []DecodedIdentity{{UIDType: tmproto.UIDTypeUID2, Bytes: nil}}
	assert.Empty(t, serviceIdentities(inbound, decoded))
}

func TestDecode_RecordsDropsByReason(t *testing.T) {
	rec := newTestRecorder()
	cfg := &TMPXSealer{
		decoders: defaultTestDecoders(t),
		recorder: rec,
	}
	cfg.Decode(t.Context(), []tmproto.IdentityToken{
		// unmapped: UIDTypeOther is not in uidToTmpxTypeID
		{UIDType: tmproto.UIDTypeOther, UserToken: "anything"},
		// no_decoder: UID2 is mapped to a TMPX type-ID but has no decoder.
		{UIDType: tmproto.UIDTypeUID2, UserToken: "any-uid2"},
		// decoder_error: HashedEmail with a too-short hex string is
		// rejected by the decoder at the input-length check.
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: "deadbeef"},
		// happy path so the test runs through to the end
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	})
	assert.Equal(t, 1, rec.dropCount(TmpxDropUnmapped, string(tmproto.UIDTypeOther)))
	assert.Equal(t, 1, rec.dropCount(TmpxDropNoDecoder, string(tmproto.UIDTypeUID2)))
	assert.Equal(t, 1, rec.dropCount(TmpxDropDecoderError, string(tmproto.UIDTypeHashedEmail)))
}

func TestSelectEntries_MAIDDecoderProducesExpectedBytes(t *testing.T) {
	// The MAID decoder is content-addressed: a canonical UUID input must
	// produce its 16 raw bytes.
	cfg := &TMPXSealer{
		priority: []tmproto.UIDType{tmproto.UIDTypeMAID},
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	}
	entries, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, tmproto.TmpxTypeMAID, entries[0].TypeID)
	assert.Equal(t, []byte{0x55, 0x0e, 0x84, 0x00, 0xe2, 0x9b, 0x41, 0xd4,
		0xa7, 0x16, 0x44, 0x66, 0x55, 0x44, 0x00, 0x00}, entries[0].Token,
		"MAID decoder must yield the raw UUID bytes")
}

func TestSealFreshNonceEachCall(t *testing.T) {
	cfg := &TMPXSealer{
		country:  "US",
		encStore: newFakeResolver(t, "k1"),
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)}}
	a, _ := cfg.Seal(t.Context(), ids)
	time.Sleep(time.Millisecond)
	b, _ := cfg.Seal(t.Context(), ids)
	assert.NotEqual(t, a, b, "two seal calls must produce distinct wire output")
}

func TestParseTmpxPriority(t *testing.T) {
	got, err := parseTmpxPriority("uid2, rampid ,id5")
	require.NoError(t, err)
	assert.Equal(t,
		[]tmproto.UIDType{tmproto.UIDTypeUID2, tmproto.UIDTypeRampID, tmproto.UIDTypeID5},
		got,
	)
}

func TestParseTmpxPriorityRejectsUnknown(t *testing.T) {
	_, err := parseTmpxPriority("uid2,not_a_real_uid_type")
	assert.Error(t, err, "unknown uid_type must be rejected")
}

func TestParseTmpxPriorityRejectsDuplicate(t *testing.T) {
	_, err := parseTmpxPriority("uid2,id5,uid2")
	assert.Error(t, err, "duplicate uid_type must be rejected")
}

func TestSelectEntries_PrioritySortsHighestFirst(t *testing.T) {
	cfg := &TMPXSealer{
		priority: []tmproto.UIDType{
			tmproto.UIDTypeMAID,
			tmproto.UIDTypeRampID,
			tmproto.UIDTypeID5,
		},
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	}
	got, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	require.Len(t, got, 3)
	wantOrder := []tmproto.TmpxTypeID{tmproto.TmpxTypeMAID, tmproto.TmpxTypeRampID, tmproto.TmpxTypeID5}
	for i, w := range wantOrder {
		assert.Equal(t, w, got[i].TypeID, "entry %d", i)
	}
}

func TestSelectEntries_DropsUidTypesNotInPriority(t *testing.T) {
	cfg := &TMPXSealer{
		priority: []tmproto.UIDType{tmproto.UIDTypeMAID},
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	}
	got, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, tmproto.TmpxTypeMAID, got[0].TypeID)
}

func TestSelectEntries_PriorityTruncatesUnderBudget(t *testing.T) {
	cfg := &TMPXSealer{
		priority: []tmproto.UIDType{
			tmproto.UIDTypeRampIDDerived, tmproto.UIDTypeRampID, tmproto.UIDTypeID5,
			tmproto.UIDTypeHashedEmail, tmproto.UIDTypeMAID,
		},
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeRampIDDerived, UserToken: validUserTokenFor(tmproto.UIDTypeRampIDDerived)},
	}
	got, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	assert.Less(t, len(got), len(ids), "expected truncation")
	for i, e := range got {
		assert.Equal(t, uidToTmpxTypeID[cfg.priority[i]], e.TypeID, "entry %d", i)
	}
	usedBytes := 0
	for _, e := range got {
		usedBytes += 1 + len(e.Token)
	}
	wire := tmproto.TmpxWireSize(tmproto.TmpxMaxKidLen, usedBytes)
	assert.LessOrEqual(t, wire, tmproto.TmpxMaxWireBytes, "selected entries within budget")
}

// TestSelectEntries_TwoSlotBudget_FitsMoreEntries pins the multi-chunk
// contract at the selection layer: an operator with two macro slots
// configured gets a 2 * TmpxMaxWireBytes wire budget, so a survivor set
// that overflows the single-slot budget (see
// TestSelectEntries_PriorityTruncatesUnderBudget) survives here without
// truncation. Priority ordering still applies.
func TestSelectEntries_TwoSlotBudget_FitsMoreEntries(t *testing.T) {
	cfg := &TMPXSealer{
		slotIDs: []string{"PIN_TMPX_1", "PIN_TMPX_2"},
		priority: []tmproto.UIDType{
			tmproto.UIDTypeRampIDDerived, tmproto.UIDTypeRampID, tmproto.UIDTypeID5,
			tmproto.UIDTypeHashedEmail, tmproto.UIDTypeMAID,
		},
		decoders: defaultTestDecoders(t),
	}
	// Same input set as TestSelectEntries_PriorityTruncatesUnderBudget: five
	// identities that overflow the single-slot budget. With two slots the
	// wire budget doubles and every survivor fits.
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeRampIDDerived, UserToken: validUserTokenFor(tmproto.UIDTypeRampIDDerived)},
	}
	got, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	assert.Equal(t, len(ids), len(got), "two-slot budget must fit every survivor that overflowed the single-slot budget")
	// The wire fits under the doubled budget but exceeds the single-slot
	// one — otherwise this test would be identical to the single-slot case.
	usedBytes := 0
	for _, e := range got {
		usedBytes += 1 + len(e.Token)
	}
	wire := tmproto.TmpxWireSize(tmproto.TmpxMaxKidLen, usedBytes)
	assert.Greater(t, wire, tmproto.TmpxMaxWireBytes, "the packed set MUST exceed a single slot's budget for this test to prove anything")
	assert.LessOrEqual(t, wire, 2*tmproto.TmpxMaxWireBytes, "packed set must fit inside the two-slot budget")
}

// TestSelectEntries_TwoSlotBudget_PriorityTruncatesOnOverflow keeps the
// priority-truncation contract for the multi-slot case: even with a
// doubled wire budget, an input set that overflows it (spec caps macro
// list at 2, so budget is 2 * TmpxMaxWireBytes) still triggers
// priority-ordered truncation rather than an error.
func TestSelectEntries_TwoSlotBudget_PriorityTruncatesOnOverflow(t *testing.T) {
	// The two-slot wire budget is 510 bytes. Every TMPX-encodable UID type
	// packed together overflows it, so priority truncation kicks in even at
	// the doubled budget.
	priority := []tmproto.UIDType{
		tmproto.UIDTypeRampIDDerived, tmproto.UIDTypeWorldIDNullifier, tmproto.UIDTypeRampID,
		tmproto.UIDTypeID5, tmproto.UIDTypeHashedEmail, tmproto.UIDTypeUID2,
		tmproto.UIDTypeEUID, tmproto.UIDTypePairID, tmproto.UIDTypePublisherFirstParty,
		tmproto.UIDTypeMAID,
	}
	cfg := &TMPXSealer{
		slotIDs:  []string{"PIN_TMPX_1", "PIN_TMPX_2"},
		priority: priority,
	}
	survivors := make([]DecodedIdentity, 0, len(priority))
	for _, uid := range priority {
		size, _ := tmproto.TmpxTokenSize(uidToTmpxTypeID[uid])
		survivors = append(survivors, DecodedIdentity{UIDType: uid, Bytes: bytesOfLen(size)})
	}
	got, err := cfg.selectEntries(survivors)
	require.NoError(t, err)
	assert.Less(t, len(got), len(survivors), "expected priority truncation under the two-slot budget")
	// Truncation drops from the tail of the priority list — the retained
	// entries MUST be the highest-priority prefix.
	for i, e := range got {
		assert.Equal(t, uidToTmpxTypeID[priority[i]], e.TypeID, "entry %d must follow priority order", i)
	}
	usedBytes := 0
	for _, e := range got {
		usedBytes += 1 + len(e.Token)
	}
	wire := tmproto.TmpxWireSize(tmproto.TmpxMaxKidLen, usedBytes)
	assert.LessOrEqual(t, wire, 2*tmproto.TmpxMaxWireBytes, "packed set must fit inside the two-slot budget")
}

// bytesOfLen returns a deterministic byte slice of the requested length —
// zero-filled is sufficient since selectEntries only sizes against the
// slice, it does not inspect content.
func bytesOfLen(n int) []byte { return make([]byte, n) }

func TestSelectEntries_NoPriorityErrorsOnOverflow(t *testing.T) {
	cfg := &TMPXSealer{decoders: defaultTestDecoders(t)}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		{UIDType: tmproto.UIDTypeRampIDDerived, UserToken: validUserTokenFor(tmproto.UIDTypeRampIDDerived)},
	}
	_, err := decodeAndSelect(t, cfg, ids)
	require.Error(t, err, "over-budget without TMPX_PRIORITY must error")
	assert.Contains(t, err.Error(), "TMPX_PRIORITY")
}

func TestSelectEntries_NoPriorityPassesUnderBudget(t *testing.T) {
	cfg := &TMPXSealer{decoders: defaultTestDecoders(t)}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
	}
	got, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// TestSeal_TwoSlotEndToEnd exercises the full seal + chunk path with a
// two-slot configuration. An identity set that overflows the single-slot
// budget succeeds under the doubled budget, and ChunkEntries returns two
// entries whose concatenation reproduces the sealed wire — the AdCP
// contract with the reassembler on the receiver side.
func TestSeal_TwoSlotEndToEnd(t *testing.T) {
	cfg := &TMPXSealer{
		country:  "US",
		encStore: newFakeResolver(t, "kid-8chr"),
		slotIDs:  []string{"PIN_TMPX_1", "PIN_TMPX_2"},
		priority: []tmproto.UIDType{
			tmproto.UIDTypeRampIDDerived, tmproto.UIDTypeRampID, tmproto.UIDTypeID5,
			tmproto.UIDTypeHashedEmail, tmproto.UIDTypeMAID,
		},
		decoders: defaultTestDecoders(t),
	}
	// Five identities that overflow the single-slot budget (see
	// TestSelectEntries_PriorityTruncatesUnderBudget which drops from this
	// same set at the 255-byte limit).
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeRampIDDerived, UserToken: validUserTokenFor(tmproto.UIDTypeRampIDDerived)},
	}
	wire, err := cfg.Seal(t.Context(), ids)
	require.NoError(t, err)
	assert.Greater(t, len(wire), tmproto.TmpxMaxWireBytes, "test only proves multi-chunk if the seal exceeds a single slot")
	assert.LessOrEqual(t, len(wire), 2*tmproto.TmpxMaxWireBytes, "seal must fit inside the two-slot budget")

	entries := cfg.ChunkEntries(wire)
	require.Len(t, entries, 2, "an over-single-slot seal must yield two macro entries")
	assert.Equal(t, "PIN_TMPX_1", entries[0].SlotID)
	assert.Equal(t, "PIN_TMPX_2", entries[1].SlotID)
	assert.Len(t, entries[0].Value, tmproto.TmpxMaxWireBytes, "first chunk MUST fill exactly one slot")
	assert.Equal(t, wire, entries[0].Value+entries[1].Value, "reassembly is byte-concatenation in slot order")
}

func TestSeal_PriorityResultsInValidWire(t *testing.T) {
	resolver := newFakeResolver(t, "kid-8chr")
	cfg := &TMPXSealer{
		country:  "US",
		encStore: resolver,
		priority: []tmproto.UIDType{
			tmproto.UIDTypeRampIDDerived, tmproto.UIDTypeRampID, tmproto.UIDTypeID5,
			tmproto.UIDTypeHashedEmail, tmproto.UIDTypeMAID,
		},
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		{UIDType: tmproto.UIDTypeRampIDDerived, UserToken: validUserTokenFor(tmproto.UIDTypeRampIDDerived)},
	}
	wire, err := cfg.Seal(t.Context(), ids)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(wire), tmproto.TmpxMaxWireBytes)
}

func TestSelectEntries_BudgetStableAcrossKidRotation(t *testing.T) {
	// The budget must be computed against TmpxMaxKidLen, not the current
	// recipient kid. Otherwise a JWKS rotation from a 1-char to an 8-char
	// kid could push a previously-fitting prefix over 255 bytes — the
	// resulting wire would silently overflow at the next refresh.
	cfg := &TMPXSealer{
		priority: []tmproto.UIDType{
			tmproto.UIDTypeRampIDDerived, tmproto.UIDTypeRampID, tmproto.UIDTypeID5,
			tmproto.UIDTypeHashedEmail, tmproto.UIDTypeMAID,
		},
		decoders: defaultTestDecoders(t),
	}
	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeRampID, UserToken: validUserTokenFor(tmproto.UIDTypeRampID)},
		{UIDType: tmproto.UIDTypeID5, UserToken: validUserTokenFor(tmproto.UIDTypeID5)},
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		{UIDType: tmproto.UIDTypeRampIDDerived, UserToken: validUserTokenFor(tmproto.UIDTypeRampIDDerived)},
	}
	got, err := decodeAndSelect(t, cfg, ids)
	require.NoError(t, err)
	// The chosen prefix must produce a valid wire at the *maximum* possible
	// kid length the buyer might rotate to.
	usedBytes := 0
	for _, e := range got {
		usedBytes += 1 + len(e.Token)
	}
	wireAtMaxKid := tmproto.TmpxWireSize(tmproto.TmpxMaxKidLen, usedBytes)
	require.LessOrEqual(t, wireAtMaxKid, tmproto.TmpxMaxWireBytes,
		"selected prefix must fit when kid grows to TmpxMaxKidLen")

	// Cross-check: the actual seal under a 1-char kid is well under budget.
	resolver := &fakeRecipientResolver{
		recipient: tmproto.TmpxRecipient{Kid: "x", PublicKey: mustEcdhPub(t)},
		ok:        true,
	}
	cfg.country = "US"
	cfg.encStore = resolver
	wire, err := cfg.Seal(t.Context(), ids)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(wire), tmproto.TmpxMaxWireBytes, "actual wire under 1-char kid")
}

func mustEcdhPub(t *testing.T) *ecdh.PublicKey {
	t.Helper()
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return sk.PublicKey()
}

// fixtureToken returns a deterministic string used as an opaque identity-graph
// input in tests. Routing the literal through a helper keeps gosec G101 from
// flagging the call site as a hardcoded credential.
func fixtureToken(scheme string) string {
	return scheme + "-input"
}

// testRecorder captures TmpxIdentityDrop calls so tests can assert per-reason
// per-uid-type drop counts. All other Recorder methods are no-ops.
type testRecorder struct {
	noopRecorder
	drops map[string]int
}

func newTestRecorder() *testRecorder { return &testRecorder{drops: map[string]int{}} }

func (r *testRecorder) TmpxIdentityDrop(_ context.Context, reason, uidType string) {
	r.drops[reason+"|"+uidType]++
}

func (r *testRecorder) dropCount(reason, uidType string) int {
	return r.drops[reason+"|"+uidType]
}

// decodeAndSelect runs the per-request decode pass and then selectEntries,
// matching the production handler flow. Tests that exercised the original
// monolithic selectEntries call use this helper so they still drive the
// same end-to-end path.
func decodeAndSelect(t *testing.T, cfg *TMPXSealer, ids []tmproto.IdentityToken) ([]tmproto.TmpxEntry, error) {
	t.Helper()
	return cfg.selectEntries(cfg.Decode(t.Context(), ids))
}

// validUserTokenFor returns a user_token string that the default decoder
// for the given UID type will accept. Used by selectEntries/Seal tests so
// the focus stays on routing/priority/sealing rather than format quirks.
//
//   - MAID          → a canonical RFC 4122 dashed UUID
//   - HashedEmail   → a 64-char hex string (content doesn't matter)
//   - ID5           → a 32-char pass-through string
//   - RampID        → any env string (the fixedLiveRampClient returns a 32-char blob)
//   - RampIDDerived → "...derived..." so the fixedLiveRampClient returns 48 chars
//
// UID types without a registered decoder (UID2, EUID, PairID,
// PublisherFirstParty) are not handled here — tests that exercise those
// types either expect them to be dropped at decode time or supply a custom
// fake decoder explicitly.
func validUserTokenFor(uid tmproto.UIDType) string {
	switch uid {
	case tmproto.UIDTypeMAID:
		return "550e8400-e29b-41d4-a716-446655440000"
	case tmproto.UIDTypeHashedEmail:
		return strings.Repeat("0", 64)
	case tmproto.UIDTypeID5:
		return "id5-canonical-token-padded--32by"
	case tmproto.UIDTypeRampIDDerived:
		return "rampid-derived-input"
	default:
		return fixtureToken(string(uid))
	}
}

// defaultTestDecoders builds the production decoder map for tests that
// construct TMPXSealer directly. A fake LiveRamp client is wired in so
// RampID and RampIDDerived identities round-trip through real-looking
// decoders rather than being silently dropped. Returned as the
// interface-typed map so the caller can also splice in fakes for
// individual UID types if needed.
func defaultTestDecoders(t *testing.T) map[tmproto.UIDType]TmpxTokenDecoder {
	t.Helper()
	raw := tmpxdecoders.NewDefaultRegistry(tmpxdecoders.RegistryOptions{
		LiveRampClient: newFixedLiveRampClient(),
	})
	out := make(map[tmproto.UIDType]TmpxTokenDecoder, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}

// fixedLiveRampClient returns the mapped value verbatim — a fixed-length
// string sized to whichever decoder is calling it. The RampID decoder
// passes the bytes through and selectEntries enforces the per-type length,
// so the fake returns 32 chars by default and 48 when the env hints
// "derived".
type fixedLiveRampClient struct{}

func newFixedLiveRampClient() *fixedLiveRampClient { return &fixedLiveRampClient{} }

func (fixedLiveRampClient) MappedID(_ context.Context, env string) (string, error) {
	size := 32
	if strings.Contains(env, "derived") {
		size = 48
	}
	return strings.Repeat("x", size), nil
}

// mustEncKeyJSON returns a JSON-shaped X25519 encryption key entry for use in
// JWKS test fixtures.
func mustEncKeyJSON(t *testing.T, kid string) map[string]any {
	t.Helper()
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return map[string]any{
		"kid":      kid,
		"kty":      "OKP",
		"crv":      "X25519",
		"x":        base64.RawURLEncoding.EncodeToString(sk.PublicKey().Bytes()),
		"use":      "enc",
		"alg":      tmproto.JWKSAlgEncryptionDHKEMX25519,
		"adcp_use": "tmpx-encrypt",
		"iat":      1,
	}
}

// testJWKSStoreFor builds a JWKSStore that talks to srv. NewJWKSStore enforces
// https:// in production paths; the helper uses srv's TLS client.
func testJWKSStoreFor(t *testing.T, srv *httptest.Server) *tmproto.JWKSStore {
	t.Helper()
	store, err := tmproto.NewJWKSStore(tmproto.JWKSStoreOptions{
		URL:        srv.URL,
		HTTPClient: srv.Client(),
	})
	require.NoError(t, err)
	require.NoError(t, store.Refresh(t.Context()))
	return store
}
