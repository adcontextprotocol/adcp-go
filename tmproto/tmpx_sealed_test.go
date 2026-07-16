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
// bound (TmpxMaxReassembledWireBytes) opens via OpenSealedCredential but is
// rejected by OpenTmpx. Guards against a future change to either bound
// silently regressing.
func TestOpenSealedCredentialLargerBound(t *testing.T) {
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Plaintext sized so the sealed wire lands comfortably above the
	// reassembled identity-token bound but within the sealed-credential
	// bound: base64url overhead is 4/3, so plaintext = TmpxMaxReassembledWireBytes
	// (in raw bytes) is more than enough to overflow the identity-token cap.
	pt := bytes.Repeat([]byte("A"), TmpxMaxReassembledWireBytes)
	wire, err := SealTmpx(TmpxRecipient{Kid: "kid-1", PublicKey: sk.PublicKey()}, nil, pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(wire) <= TmpxMaxReassembledWireBytes {
		t.Fatalf("test precondition: wire len %d must exceed TmpxMaxReassembledWireBytes %d", len(wire), TmpxMaxReassembledWireBytes)
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
		t.Fatalf("OpenTmpx must reject a wire above the reassembled bound with 'exceeds maximum', got err=%v", err)
	}
}

// TestOpenTmpxAcceptsReassembledMultiChunk pins the invariant Blocker 1 fixed:
// a sealed wire larger than one macro slot (TmpxMaxWireBytes) but within the
// two-slot budget round-trips through OpenTmpx when the receiver reassembles
// the chunks by byte-concatenation in slot order. Before the fix OpenTmpx
// rejected such tokens — the sealer emitted artifacts its own conformant
// receiver refused.
func TestOpenTmpxAcceptsReassembledMultiChunk(t *testing.T) {
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Plaintext sized so the sealed wire exceeds one macro slot but fits in
	// two. base64url is 4/3 expansion; ~200 raw bytes plus 48 bytes of HPKE
	// overhead reliably overflows the 255-byte single-slot bound while
	// staying under 2*255 = 510.
	pt := bytes.Repeat([]byte("A"), 200)
	wire, err := SealTmpx(TmpxRecipient{Kid: "kid-1", PublicKey: sk.PublicKey()}, nil, pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(wire) <= TmpxMaxWireBytes {
		t.Fatalf("test precondition: wire len %d must exceed one slot (%d)", len(wire), TmpxMaxWireBytes)
	}
	if len(wire) > TmpxMaxReassembledWireBytes {
		t.Fatalf("test precondition: wire len %d must fit in %d slots (%d)", len(wire), TmpxMaxSlots, TmpxMaxReassembledWireBytes)
	}

	// Simulate the receiver reassembling chunks from macro slots: split at
	// the TmpxMaxWireBytes boundary and rejoin. Reassembly is exactly byte
	// concatenation in slot order (the AdCP contract).
	chunk1 := wire[:TmpxMaxWireBytes]
	chunk2 := wire[TmpxMaxWireBytes:]
	reassembled := chunk1 + chunk2
	if reassembled != wire {
		t.Fatalf("reassembly must be byte-identical to the sealed wire")
	}

	got, kid, err := OpenTmpx(sk, nil, reassembled)
	if err != nil {
		t.Fatalf("OpenTmpx must accept the reassembled multi-chunk wire: %v", err)
	}
	if kid != "kid-1" {
		t.Fatalf("kid = %q, want kid-1", kid)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("OpenTmpx round-trip mismatch after reassembly")
	}
}
