package nitro

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Document is the CBOR payload of a Nitro attestation COSE_Sign1. Field
// tags follow the map keys AWS uses in the on-wire format — string keys,
// not integer, per the AWS Nitro attestation-document format.
//
// See AWS docs: "Attestation Document" under Nitro Enclaves.
type Document struct {
	ModuleID    string            `cbor:"module_id"`
	Timestamp   uint64            `cbor:"timestamp"`
	Digest      string            `cbor:"digest"`
	PCRs        map[uint32][]byte `cbor:"pcrs"`
	Certificate []byte            `cbor:"certificate"`
	CABundle    [][]byte          `cbor:"cabundle"`
	PublicKey   []byte            `cbor:"public_key,omitempty"`
	UserData    []byte            `cbor:"user_data,omitempty"`
	Nonce       []byte            `cbor:"nonce,omitempty"`
}

// documentAlias is a struct-tagged twin of Document without a MarshalCBOR
// method — used inside Document.MarshalCBOR to break the recursion the
// cbor library would otherwise land in (calling the method to encode the
// value it's already encoding).
type documentAlias struct {
	ModuleID    string            `cbor:"module_id"`
	Timestamp   uint64            `cbor:"timestamp"`
	Digest      string            `cbor:"digest"`
	PCRs        map[uint32][]byte `cbor:"pcrs"`
	Certificate []byte            `cbor:"certificate"`
	CABundle    [][]byte          `cbor:"cabundle"`
	PublicKey   []byte            `cbor:"public_key,omitempty"`
	UserData    []byte            `cbor:"user_data,omitempty"`
	Nonce       []byte            `cbor:"nonce,omitempty"`
}

// MarshalCBOR emits deterministic CBOR (Core Deterministic Encoding).
// AWS's real NSM output is Core Deterministic; matching that in the mock
// keeps the verifier code path byte-for-byte identical.
func (d Document) MarshalCBOR() ([]byte, error) {
	enc, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		return nil, fmt.Errorf("nitro: build CBOR encoder: %w", err)
	}
	return enc.Marshal(documentAlias(d))
}

// UnmarshalDocumentPayload parses the CBOR payload of a Nitro attestation
// document. Called by the verifier after it has cracked open the COSE_Sign1.
func UnmarshalDocumentPayload(payload []byte) (*Document, error) {
	var doc Document
	if err := cbor.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("nitro: parse document payload: %w", err)
	}
	if doc.ModuleID == "" {
		return nil, fmt.Errorf("nitro: document missing module_id")
	}
	if doc.Digest == "" {
		return nil, fmt.Errorf("nitro: document missing digest")
	}
	if len(doc.PCRs) == 0 {
		return nil, fmt.Errorf("nitro: document has no PCRs")
	}
	if len(doc.Certificate) == 0 {
		return nil, fmt.Errorf("nitro: document missing certificate")
	}
	return &doc, nil
}
