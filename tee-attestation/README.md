# tee-attestation (prototype)

Prototype implementation of the TMP router attestation wire shape proposed in [adcontextprotocol/adcp#5770](https://github.com/adcontextprotocol/adcp/pull/5770).

**This is not production code.** It exists to answer the question the spec's slot-projection wording glosses over: what does the per-format byte layout for the binding rule look like against a real Nitro attestation document, end-to-end? The answer is the finding in [`nitro/PROJECTION.md`](./nitro/PROJECTION.md) — that's the payload back into the spec's slot-projection registry item.

## What's here

- **Top-level (`tee-attestation`)** — envelope shape, failure-mode enum, minimal JWK type, RFC 7638 thumbprint helper. Format-independent; the same types apply once TDX/SEV-SNP/GCP verifier kits arrive.
- **`nitro/`** — AWS Nitro Enclaves format only.
  - `nsm.go` — `Nsm` interface (matches the AWS Nitro NSM `AttestationDoc` request shape).
  - `mock.go` — mock `Nsm` that generates real COSE_Sign1 documents signed by a caller-owned test CA. Same wire format as a production Nitro document, which means the verifier code path is byte-for-byte identical between mock and prod.
  - `nsm_real.go` — placeholder for the real /dev/nsm-backed impl, behind a `nitro` build tag. Wiring lands in a follow-up.
  - `document.go` — CBOR payload struct + deterministic-encoding marshaler.
  - `emit.go` — `Emit(ctx, nsm, req) → Envelope`. Uses Nitro's dedicated `nonce` and `public_key` fields per PROJECTION.md.
  - `verify.go` — the read side. Walks the 9-step verification flow from `docs/trusted-match/router-attestation.mdx` and returns typed `VerifyError`s that name the failure modes from the spec.

## Round-trip evidence

`roundtrip_test.go` exercises emit → verify plus every failure mode from the spec's failure-mode table:

| Test | Failure mode from spec |
|---|---|
| `TestNitroRoundTrip` | *(happy path)* |
| `TestVerifyRejectsNonceMismatch` | `nonce_mismatch` |
| `TestVerifyRejectsExpiredEnvelope` | `envelope_expired` |
| `TestVerifyRejectsUnsupportedFormat` | `unsupported_format` |
| `TestVerifyRejectsTamperedDocument` | `platform_verification_failed` |
| `TestVerifyRejectsSwappedSigningKey` | `signing_key_not_bound` |
| `TestVerifyRejectsWrongRoot` | `platform_verification_failed` |
| `TestVerifyRejectsPolicyDisallow` | `measurement_disallowed` |
| `TestVerifyAcceptsPerRequestPathWithoutNonceEcho` | *(per-request `X-TMP-Attestation` path)* |
| `TestEnvelopeJSONRoundTrip` | *(wire format stability)* |
| `TestJWKThumbprintStable` | *(RFC 7638 canonicalization)* |

All pass on the mock. Running against a real Nitro instance is the next step (see "Not implemented").

## What this tells us about the spec

See `nitro/PROJECTION.md` for the full write-up. Short version:

1. **The spec's "same user-data slot" wording is a Nitro-specific category error.** Nitro has three distinct fields (`nonce`, `public_key`, `user_data`); using them directly is cleaner than inventing a synthetic packing convention. The spec should carry a **per-format normative table** (Nitro / TDX / SEV-SNP / GCP), not a single "slot" description.
2. **Raw pubkey bytes in Nitro's `public_key` field is the right projection.** Not a JCS-encoded JWK; not a thumbprint. Nitro's `public_key` field is designed for exactly this use.
3. **The RFC 7638 thumbprint stays in the verification recipe but is no longer load-bearing for Nitro.** With raw pubkey bytes on the wire, byte-comparison is sufficient; thumbprint agreement is a redundant sanity check (implemented in `verify.go` as a canary against future canonicalization drift on either side).

## Not implemented

- **Real Nitro NSM.** `nsm_real.go` is a stub behind the `nitro` build tag. Real wiring against `aws-nitro-enclaves-nsm-api` follows this PR.
- **Router integration.** `cmd/router` and `router/` do not use this package. Wiring in lands once the byte layout in the spec is settled — otherwise every wire change ripples into production code.
- **TDX / SEV-SNP / GCP Confidential Space.** Nitro-only for the initial finding.
- **`X-TMP-Attestation` per-request header carrier.** Envelope + verifier first; the header carrier is thin glue on top and lands after the spec-side questions close.
- **KMS-bound key custody.** The mock generates its own keypair. Real in-enclave key generation with KMS release-only-to-attested-workload is separate.

## Testing

```
go test ./tee-attestation/...
```

Every test runs against the mock, on any machine — no Nitro tooling required.
