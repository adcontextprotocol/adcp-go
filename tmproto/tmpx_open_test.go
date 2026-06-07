package tmproto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
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
	b := []byte(wire)
	last := len(b) - 1
	if b[last] == 'A' {
		b[last] = 'B'
	} else {
		b[last] = 'A'
	}
	if _, _, err := OpenTmpx(skR, nil, string(b)); err == nil {
		t.Fatal("expected Open to fail on tampered ciphertext, got nil error")
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
