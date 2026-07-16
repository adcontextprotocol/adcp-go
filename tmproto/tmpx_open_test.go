package tmproto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func newX25519(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate X25519 key: %v", err)
	}
	return sk
}

// TestSealOpenRoundTrip seals a token to a recipient public key and opens it
// with the matching private key, asserting the plaintext and kid round-trip.
func TestSealOpenRoundTrip(t *testing.T) {
	skR := newX25519(t)
	recipient := TmpxRecipient{Kid: "k1", PublicKey: skR.PublicKey()}
	plaintext := []byte("resolved-identity-token-set")

	wire, err := SealTmpx(recipient, nil, plaintext)
	if err != nil {
		t.Fatalf("SealTmpx: %v", err)
	}
	if !strings.HasPrefix(wire, "k1.") {
		t.Fatalf("wire missing kid prefix: %q", wire)
	}

	got, kid, err := OpenTmpx(skR, nil, wire)
	if err != nil {
		t.Fatalf("OpenTmpx: %v", err)
	}
	if kid != "k1" {
		t.Errorf("kid = %q, want k1", kid)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

// TestOpenTamperFails confirms a modified ciphertext fails authentication.
func TestOpenTamperFails(t *testing.T) {
	skR := newX25519(t)
	wire, err := SealTmpx(TmpxRecipient{Kid: "k1", PublicKey: skR.PublicKey()}, nil, []byte("hello"))
	if err != nil {
		t.Fatalf("SealTmpx: %v", err)
	}
	dot := strings.IndexByte(wire, '.')
	payload, err := base64.RawURLEncoding.DecodeString(wire[dot+1:])
	if err != nil {
		t.Fatalf("decode sealed payload: %v", err)
	}
	payload[len(payload)-1] ^= 0x01
	tampered := wire[:dot+1] + base64.RawURLEncoding.EncodeToString(payload)
	if _, _, err := OpenTmpx(skR, nil, tampered); err == nil {
		t.Fatal("expected Open to fail on tampered ciphertext, got nil error")
	}
}

// TestOpenRejectsNonCanonicalBase64 confirms Open rejects alternate raw
// base64url spellings that decode to the same bytes under Go's non-strict
// decoder. The final quantum can carry unused low bits; accepting those would
// allow multiple wire strings for one TMPX token.
func TestOpenRejectsNonCanonicalBase64(t *testing.T) {
	skR := newX25519(t)
	wire, err := SealTmpx(TmpxRecipient{Kid: "k1", PublicKey: skR.PublicKey()}, nil, []byte("hello"))
	if err != nil {
		t.Fatalf("SealTmpx: %v", err)
	}
	dot := strings.IndexByte(wire, '.')
	wantPayload, err := base64.RawURLEncoding.DecodeString(wire[dot+1:])
	if err != nil {
		t.Fatalf("decode sealed payload: %v", err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, c := range alphabet {
		alias := wire[:len(wire)-1] + string(c)
		if alias == wire {
			continue
		}
		gotPayload, err := base64.RawURLEncoding.DecodeString(alias[dot+1:])
		if err != nil || !bytes.Equal(gotPayload, wantPayload) {
			continue
		}
		if _, _, err := OpenTmpx(skR, nil, alias); err == nil {
			t.Fatal("expected Open to reject non-canonical base64url payload, got nil error")
		}
		return
	}
	t.Fatal("test setup did not find a non-canonical base64url alias")
}

func TestTmpxKid(t *testing.T) {
	skR := newX25519(t)
	wire, err := SealTmpx(TmpxRecipient{Kid: "k1", PublicKey: skR.PublicKey()}, nil, []byte("hello"))
	if err != nil {
		t.Fatalf("SealTmpx: %v", err)
	}
	kid, err := TmpxKid(wire)
	if err != nil {
		t.Fatalf("TmpxKid: %v", err)
	}
	if kid != "k1" {
		t.Fatalf("kid = %q, want k1", kid)
	}
	if _, err := TmpxKid("bad*kid.payload"); err == nil {
		t.Fatal("expected invalid kid prefix to fail")
	}
}

func TestOpenReturnsKidOnPayloadError(t *testing.T) {
	skR := newX25519(t)
	_, kid, err := OpenTmpx(skR, nil, "k1.A")
	if err == nil {
		t.Fatal("expected base64 error")
	}
	if kid != "k1" {
		t.Fatalf("kid = %q, want k1", kid)
	}
}

func TestOpenRejectsOversizedWireBeforeDecode(t *testing.T) {
	skR := newX25519(t)
	// Bound is TmpxMaxReassembledWireBytes — the maximum reassembled
	// multi-chunk token the receiver ever needs to accept. Anything above it
	// is a protocol violation and must be rejected before base64 decode.
	wire := "k1." + strings.Repeat("A", TmpxMaxReassembledWireBytes)
	if _, _, err := OpenTmpx(skR, nil, wire); err == nil {
		t.Fatal("expected oversized wire to fail")
	}
}

// TestOpenWrongKeyFails confirms Open fails under a different recipient key.
func TestOpenWrongKeyFails(t *testing.T) {
	skR := newX25519(t)
	other := newX25519(t)
	wire, err := SealTmpx(TmpxRecipient{Kid: "k1", PublicKey: skR.PublicKey()}, nil, []byte("hello"))
	if err != nil {
		t.Fatalf("SealTmpx: %v", err)
	}
	if _, _, err := OpenTmpx(other, nil, wire); err == nil {
		t.Fatal("expected Open to fail under a different recipient key, got nil error")
	}
}
