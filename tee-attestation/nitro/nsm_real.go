//go:build nitro

package nitro

import "context"

// RealNsm is the /dev/nsm-backed implementation used inside a Nitro Enclave.
// It is intentionally a stub in this prototype — validating the wire shape
// against a real Nitro instance is the follow-up to this package.
//
// Wiring notes (for the follow-up):
//   - Depend on aws-nitro-enclaves-nsm-api (Go bindings via CGO, or the Rust
//     binary via `nsm-cli`).
//   - Attest maps AttestRequest.Nonce/PublicKey/UserData directly onto the
//     NSM's AttestationDoc request fields.
//   - Real NSM returns the COSE_Sign1 wrapped attestation document; the
//     verifier code path here is byte-identical between mock and real.
type RealNsm struct{}

func (RealNsm) Attest(_ context.Context, _ AttestRequest) ([]byte, error) {
	return nil, ErrNotImplemented
}

var _ Nsm = RealNsm{}
