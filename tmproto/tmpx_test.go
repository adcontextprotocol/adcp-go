package tmproto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// hpkeOpenBase is a test-only HPKE Open in mode_base for the TMPX cipher
// suite — used for roundtrip verification. Mirrors hpkeSealBase but recipient
// uses skR with the encapsulated pkE.
func hpkeOpenBase(skR *ecdh.PrivateKey, enc, info, aad, ct []byte) ([]byte, error) {
	pkE, err := ecdh.X25519().NewPublicKey(enc)
	if err != nil {
		return nil, err
	}
	dh, err := skR.ECDH(pkE)
	if err != nil {
		return nil, err
	}

	pkRBytes := skR.PublicKey().Bytes()
	suiteID := buildHPKESuiteID(hpkeKEMX25519HKDFSHA256, hpkeKDFHKDFSHA256, hpkeAEADChaCha20Poly)
	kemSuiteID := buildHPKEKEMSuiteID(hpkeKEMX25519HKDFSHA256)

	kemContext := append([]byte{}, enc...)
	kemContext = append(kemContext, pkRBytes...)
	sharedSecret, err := dhkemExtractAndExpand(dh, kemContext, kemSuiteID, hpkeNh)
	if err != nil {
		return nil, err
	}

	pskIDHash, err := labeledExtract(nil, []byte("psk_id_hash"), nil, suiteID)
	if err != nil {
		return nil, err
	}
	infoHash, err := labeledExtract(nil, []byte("info_hash"), info, suiteID)
	if err != nil {
		return nil, err
	}
	keyScheduleContext := append([]byte{hpkeModeBase}, pskIDHash...)
	keyScheduleContext = append(keyScheduleContext, infoHash...)

	secret, err := labeledExtract(sharedSecret, []byte("secret"), nil, suiteID)
	if err != nil {
		return nil, err
	}
	key, err := labeledExpand(secret, []byte("key"), keyScheduleContext, hpkeNk, suiteID)
	if err != nil {
		return nil, err
	}
	baseNonce, err := labeledExpand(secret, []byte("base_nonce"), keyScheduleContext, hpkeNn, suiteID)
	if err != nil {
		return nil, err
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, baseNonce, ct, aad)
}

// TestHPKERFC9180A3Vector validates the implementation against the test
// vector in RFC 9180 Appendix A.3 (mode_base, KEM=DHKEM(X25519,HKDF-SHA256),
// KDF=HKDF-SHA256, AEAD=ChaCha20-Poly1305, "Ode on a Grecian Urn" info,
// "Beauty is truth, truth beauty" plaintext, AAD "Count-0").
func TestHPKERFC9180A3Vector(t *testing.T) {
	skEm := mustHex(t, "f4ec9b33b792c372c1d2c2063507b684ef925b8c75a42dbcbf57d63ccd381600")
	pkRm := mustHex(t, "4310ee97d88cc1f088a5576c77ab0cf5c3ac797f3d95139c6c84b5429c59662a")
	info := mustHex(t, "4f6465206f6e2061204772656369616e2055726e")
	wantEnc := mustHex(t, "1afa08d3dec047a643885163f1180476fa7ddb54c6a8029ea33f95796bf2ac4a")
	wantSharedSecret := mustHex(t, "0bbe78490412b4bbea4812666f7916932b828bba79942424abb65244930d69a7")
	wantSecret := mustHex(t, "5b9cd775e64b437a2335cf499361b2e0d5e444d5cb41a8a53336d8fe402282c6")
	wantKey := mustHex(t, "ad2744de8e17f4ebba575b3f5f5a8fa1f69c2a07f6e7500bc60ca6e3e3ec1c91")
	wantBaseNonce := mustHex(t, "5c4d98150661b848853b547f")

	pt := mustHex(t, "4265617574792069732074727574682c20747275746820626561757479")
	aad := mustHex(t, "436f756e742d30")
	wantCt := mustHex(t, "1c5250d8034ec2b784ba2cfd69dbdb8af406cfe3ff938e131f0def8c8b60b4db21993c62ce81883d2dd1b51a28")

	pkR, err := ecdh.X25519().NewPublicKey(pkRm)
	if err != nil {
		t.Fatalf("parse pkR: %v", err)
	}
	skE, err := ecdh.X25519().NewPrivateKey(skEm)
	if err != nil {
		t.Fatalf("parse skE: %v", err)
	}

	enc, ct, err := hpkeSealBase(pkR, skE, info, aad, pt)
	if err != nil {
		t.Fatalf("hpkeSealBase: %v", err)
	}
	if !bytes.Equal(enc, wantEnc) {
		t.Fatalf("enc mismatch:\ngot  %x\nwant %x", enc, wantEnc)
	}
	if !bytes.Equal(ct, wantCt) {
		t.Fatalf("ct mismatch:\ngot  %x\nwant %x", ct, wantCt)
	}

	// Cross-check intermediate KDF values to localize regressions.
	pkE := skE.PublicKey()
	dh, err := skE.ECDH(pkR)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	kemSuiteID := buildHPKEKEMSuiteID(hpkeKEMX25519HKDFSHA256)
	kemContext := append([]byte{}, pkE.Bytes()...)
	kemContext = append(kemContext, pkR.Bytes()...)
	sharedSecret, _ := dhkemExtractAndExpand(dh, kemContext, kemSuiteID, hpkeNh)
	if !bytes.Equal(sharedSecret, wantSharedSecret) {
		t.Errorf("shared_secret mismatch:\ngot  %x\nwant %x", sharedSecret, wantSharedSecret)
	}

	suiteID := buildHPKESuiteID(hpkeKEMX25519HKDFSHA256, hpkeKDFHKDFSHA256, hpkeAEADChaCha20Poly)
	secret, _ := labeledExtract(sharedSecret, []byte("secret"), nil, suiteID)
	if !bytes.Equal(secret, wantSecret) {
		t.Errorf("secret mismatch:\ngot  %x\nwant %x", secret, wantSecret)
	}

	// Re-derive key/nonce from secret to keep the assertion specific.
	pskIDHash, _ := labeledExtract(nil, []byte("psk_id_hash"), nil, suiteID)
	infoHash, _ := labeledExtract(nil, []byte("info_hash"), info, suiteID)
	ksc := append([]byte{hpkeModeBase}, pskIDHash...)
	ksc = append(ksc, infoHash...)
	gotKey, _ := labeledExpand(secret, []byte("key"), ksc, hpkeNk, suiteID)
	gotNonce, _ := labeledExpand(secret, []byte("base_nonce"), ksc, hpkeNn, suiteID)
	if !bytes.Equal(gotKey, wantKey) {
		t.Errorf("key mismatch:\ngot  %x\nwant %x", gotKey, wantKey)
	}
	if !bytes.Equal(gotNonce, wantBaseNonce) {
		t.Errorf("base_nonce mismatch:\ngot  %x\nwant %x", gotNonce, wantBaseNonce)
	}

	// Sanity: consume hkdf to silence unused-import detection if labeledExpand
	// gets refactored to inline.
	_, _ = hkdf.Extract(sha256.New, []byte{0}, nil)
	_ = binary.BigEndian
}

// TestHPKESealOpenRoundtrip verifies the implementation is internally
// consistent — Open undoes Seal across many random inputs.
func TestHPKESealOpenRoundtrip(t *testing.T) {
	skR, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkR := skR.PublicKey()
	for i := range 32 {
		pt := make([]byte, 1+i*7)
		_, _ = rand.Read(pt)
		info := []byte("test-info")
		aad := []byte("test-aad")
		skE, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("ephemeral key[%d]: %v", i, err)
		}
		enc, ct, err := hpkeSealBase(pkR, skE, info, aad, pt)
		if err != nil {
			t.Fatalf("Seal[%d]: %v", i, err)
		}
		got, err := hpkeOpenBase(skR, enc, info, aad, ct)
		if err != nil {
			t.Fatalf("Open[%d]: %v", i, err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("roundtrip[%d]: got %x, want %x", i, got, pt)
		}
	}
}

func TestSealTmpxRoundtrip(t *testing.T) {
	skR, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	entries := []TmpxEntry{
		{TypeID: TmpxTypeUID2, Token: bytes.Repeat([]byte{0xA1}, 32)},
		{TypeID: TmpxTypeMAID, Token: bytes.Repeat([]byte{0xB2}, 16)},
	}
	plaintext, err := EncodeTmpxPlaintext("US", entries, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	wire, err := SealTmpx(TmpxRecipient{Kid: "k1", PublicKey: skR.PublicKey()}, nil, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	kid, payload, ok := strings.Cut(wire, ".")
	if !ok || kid != "k1" {
		t.Fatalf("wire format: %q", wire)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(raw) < 32+16 {
		t.Fatalf("payload too short: %d bytes", len(raw))
	}
	encB := raw[:32]
	ct := raw[32:]
	got, err := hpkeOpenBase(skR, encB, nil, nil, ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("roundtrip mismatch:\ngot  %x\nwant %x", got, plaintext)
	}

	// Validate the decrypted plaintext layout.
	if got[0] != TmpxFormatVersion {
		t.Errorf("version byte: got %d, want %d", got[0], TmpxFormatVersion)
	}
	if string(got[5:7]) != "US" {
		t.Errorf("country: got %q, want US", got[5:7])
	}
	if int(got[15]) != len(entries) {
		t.Errorf("count: got %d, want %d", got[15], len(entries))
	}
}

func TestEncodeTmpxPlaintextHeaderShape(t *testing.T) {
	// Inject a deterministic nonce so the header bytes can be asserted.
	rd := bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8, 0, 0, 0, 0, 0, 0, 0, 0})
	pt, err := encodeTmpxPlaintextWith("DE", []TmpxEntry{
		{TypeID: TmpxTypeUID2, Token: bytes.Repeat([]byte{0xCC}, 32)},
	}, time.Unix(0x11223344, 0), rd)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []byte{
		0x01,                   // version
		0x11, 0x22, 0x33, 0x44, // ts
		'D', 'E', // country
		1, 2, 3, 4, 5, 6, 7, 8, // nonce
		1, // count
	}
	if !bytes.Equal(pt[:16], wantHeader) {
		t.Fatalf("header: got %x, want %x", pt[:16], wantHeader)
	}
	if pt[16] != byte(TmpxTypeUID2) {
		t.Errorf("entry type id: got %d, want %d", pt[16], TmpxTypeUID2)
	}
}

func TestEncodeTmpxPlaintextRejectsBadCountry(t *testing.T) {
	for _, c := range []string{"", "u", "US ", "us", "U1"} {
		_, err := EncodeTmpxPlaintext(c, nil, time.Now())
		if err == nil {
			t.Errorf("country %q must be rejected", c)
		}
	}
	_, err := EncodeTmpxPlaintext("LEAKY", nil, time.Now())
	if err == nil {
		t.Fatal("country LEAKY must be rejected")
	}
	if strings.Contains(err.Error(), "LEAKY") {
		t.Fatalf("country error echoed rejected value: %q", err.Error())
	}
}

func TestEncodeTmpxPlaintextRejectsWrongTokenSize(t *testing.T) {
	_, err := EncodeTmpxPlaintext("US", []TmpxEntry{
		{TypeID: TmpxTypeUID2, Token: []byte("too short")},
	}, time.Now())
	if err == nil {
		t.Fatal("expected error for wrong token size")
	}
}

func TestEncodeTmpxPlaintextRejectsUnknownType(t *testing.T) {
	_, err := EncodeTmpxPlaintext("US", []TmpxEntry{
		{TypeID: TmpxTypeID(200), Token: bytes.Repeat([]byte{0}, 32)},
	}, time.Now())
	if err == nil {
		t.Fatal("expected error for unknown type id")
	}
}

func TestSealTmpxKidValidation(t *testing.T) {
	skR, _ := ecdh.X25519().GenerateKey(rand.Reader)
	rcp := TmpxRecipient{Kid: "", PublicKey: skR.PublicKey()}
	if _, err := SealTmpx(rcp, nil, []byte("x")); err == nil {
		t.Error("empty kid must be rejected")
	}
	rcp.Kid = "abcdefghi" // 9 chars, exceeds spec max of 8
	if _, err := SealTmpx(rcp, nil, []byte("x")); err == nil {
		t.Error("9-char kid must be rejected")
	}
}

func TestTmpxWireSizeSpecExample(t *testing.T) {
	// Spec §"TMPX Exposure Tokens" / "Size budget":
	//   "Three 32-byte tokens = 99 bytes — fits comfortably." (entries bytes)
	// HPKE overhead 48 + header 16 + entries 99 = 163 → base64url 218 chars.
	// With an 8-char kid plus separator: 8 + 1 + 218 = 227 ≤ 255 ✓
	entriesBytes := 3 * (1 + 32)
	got := TmpxWireSize(8, entriesBytes)
	if got != 227 {
		t.Errorf("TmpxWireSize(8, %d) = %d, want 227", entriesBytes, got)
	}
	if got > TmpxMaxWireBytes {
		t.Fatalf("spec example overflows budget: %d > %d", got, TmpxMaxWireBytes)
	}
}

func TestTmpxWireSizeEmptyEntries(t *testing.T) {
	// kidLen=1, no entries: 1 + 1 + base64(16+48) = 2 + 86 = 88
	got := TmpxWireSize(1, 0)
	if got != 88 {
		t.Errorf("TmpxWireSize(1, 0) = %d, want 88", got)
	}
}

func TestTmpxTokenSizeRegistry(t *testing.T) {
	// Spec: types 1..4, 7, 8, 9 are 32 bytes; 5 is 48; 6 is 16.
	cases := map[TmpxTypeID]int{
		TmpxTypeUID2: 32, TmpxTypeEUID: 32, TmpxTypeID5: 32,
		TmpxTypeRampID: 32, TmpxTypeRampIDDerived: 48,
		TmpxTypeMAID: 16, TmpxTypePairID: 32,
		TmpxTypeHashedEmail: 32, TmpxTypePublisherFirstParty: 32,
	}
	for id, want := range cases {
		got, ok := TmpxTokenSize(id)
		if !ok || got != want {
			t.Errorf("TmpxTokenSize(%d) = (%d, %v), want (%d, true)", id, got, ok, want)
		}
	}
	if _, ok := TmpxTokenSize(TmpxTypeID(200)); ok {
		t.Errorf("unknown type id must report false")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}
