// Package teeattestation is a PROTOTYPE implementation of the TMP router
// attestation wire shape proposed in adcontextprotocol/adcp#5770.
//
// The purpose of this package is NOT production use. It exists to answer the
// question the spec's slot-projection wording glosses over: what does the
// per-format byte layout for the [binding rule] actually look like against a
// real Nitro attestation document, end-to-end? The answer is meant to feed
// back into the spec — see nitro/PROJECTION.md for the finding.
//
// Structure:
//
//   - Top-level: envelope shape, failure-mode enum, JWK thumbprint helper.
//     Format-independent; the same types would apply once TDX/SEV-SNP/GCP
//     verifier kits arrive.
//   - nitro/: AWS Nitro Enclaves format. Emit path (Nsm interface, mock impl
//     that generates real COSE_Sign1 documents signed by a test CA, and a
//     stub real-Nitro impl behind a `nitro` build tag). Verify path
//     (full COSE_Sign1 + cert-chain + measurement extraction + binding).
//
// Non-goals for this prototype:
//   - Router integration (cmd/router, router/). Prove the shape works
//     stand-alone first; wire in once the byte layout is settled.
//   - TDX / SEV-SNP / GCP Confidential Space. Nitro-only for the finding.
//   - Real Nitro NSM. The real impl behind the `nitro` build tag is a
//     placeholder; running against a real Nitro instance lands in a
//     follow-up.
//   - X-TMP-Attestation header carrier (per-request attestation). Prove
//     the envelope+verify path first.
//   - KMS-bound key custody. The mock generates its own keypair; the
//     real path plugs into an in-enclave key generator later.
//
// [binding rule]: https://github.com/adcontextprotocol/adcp/blob/main/docs/trusted-match/router-attestation.mdx
package teeattestation
