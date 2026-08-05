// Package nitro implements the AWS Nitro Enclaves branch of the TMP router
// attestation wire shape (spec PR adcontextprotocol/adcp#5770). It contains
// the emit path (Nsm interface + a mock impl that generates real
// COSE_Sign1 documents signed by a test CA) and the verify path (full
// COSE_Sign1 + cert-chain-to-root + measurement extraction + binding rule).
//
// See PROJECTION.md for the byte-layout decisions this package makes for
// Nitro-specific fields (`nonce`, `public_key`, `user_data`) — this is the
// finding meant to feed back into the spec's slot-projection registry.
package nitro

import (
	"context"
	"errors"
)

// Nsm is the Nitro Security Module surface the emit path calls into. In an
// enclave, the real impl talks to /dev/nsm via `nsm-lib`. Out of an enclave
// (tests, CI, this prototype), the mock impl in mock.go generates equivalent
// COSE_Sign1 documents signed by a test CA — same wire format so the
// verifier code path is byte-for-byte identical between mock and prod.
type Nsm interface {
	// Attest requests an attestation document from the NSM.
	//
	// The three optional fields map to the Nitro NSM Attestation request
	// per aws-nitro-enclaves-nsm-api:
	//   - Nonce      — an opaque value the caller supplies; the NSM echoes
	//                  it into the document's `nonce` field.
	//   - PublicKey  — an opaque public-key blob the enclave wants attested;
	//                  the NSM echoes it into the document's `public_key`
	//                  field. Max 1024 bytes.
	//   - UserData   — arbitrary bytes; echoed into `user_data`.
	//                  Max 512 bytes.
	//
	// The returned bytes are the COSE_Sign1-wrapped Nitro attestation
	// document, ready to be base64url-encoded into the envelope's
	// `attestation_document` field.
	Attest(ctx context.Context, req AttestRequest) ([]byte, error)
}

// AttestRequest matches the Nitro NSM AttestationDoc request fields. Any of
// the fields may be nil; unset fields are omitted from the resulting
// document.
type AttestRequest struct {
	Nonce     []byte
	PublicKey []byte
	UserData  []byte
}

// ErrNotImplemented is returned by the real-Nitro stub when the build was
// not made with the `nitro` build tag. Kept in the interface package so
// callers can errors.Is against it without pulling the real-impl file into
// non-Nitro builds.
var ErrNotImplemented = errors.New("nitro: real NSM impl requires the `nitro` build tag and a Nitro Enclaves host")
