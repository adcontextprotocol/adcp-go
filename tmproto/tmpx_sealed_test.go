package tmproto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"strings"
	"testing"
)

// TestOpenSealedCredentialLargerBound proves the distinguishing behavior of the
// sealed-credential open path: a wire string larger than the identity-token
// bound (TmpxMaxWireBytes) opens via OpenSealedCredential but is rejected by
// OpenTmpx. Guards against a future change to either bound silently regressing.
func TestOpenSealedCredentialLargerBound(t *testing.T) {
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// 300-byte plaintext seals to a wire comfortably above the 255-byte
	// identity-token bound but within the sealed-credential bound.
	pt := bytes.Repeat([]byte("A"), 300)
	wire, err := SealTmpx(TmpxRecipient{Kid: "kid-1", PublicKey: sk.PublicKey()}, nil, pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(wire) <= TmpxMaxWireBytes {
		t.Fatalf("test precondition: wire len %d must exceed TmpxMaxWireBytes %d", len(wire), TmpxMaxWireBytes)
	}
	if len(wire) > TmpxSealedMaxWireBytes {
		t.Fatalf("test precondition: wire len %d must be within TmpxSealedMaxWireBytes %d", len(wire), TmpxSealedMaxWireBytes)
	}

	got, _, err := OpenSealedCredential(sk, nil, wire)
	if err != nil {
		t.Fatalf("OpenSealedCredential must open a sealed-sized wire: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("OpenSealedCredential round-trip mismatch")
	}

	if _, _, err := OpenTmpx(sk, nil, wire); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("OpenTmpx must reject the over-255-byte wire with 'exceeds maximum', got err=%v", err)
	}
}
