# Nitro slot projection

**Finding for adcontextprotocol/adcp#5770's slot-projection registry item.**

## What the spec says today

The normative page (`docs/trusted-match/router-attestation.mdx`, "Binding rule" and "Nonce requirements") describes a single "platform user-data slot" that carries both the nonce and the JWK thumbprint, and delegates the byte layout to per-format verifier kits. That framing is a **category mismatch for Nitro** — a real Nitro attestation document has three separate, dedicated fields (`nonce`, `public_key`, `user_data`), not one shared slot.

## What this package does

This prototype uses Nitro's dedicated fields directly:

| Envelope value | Nitro doc field | Why |
|---|---|---|
| `nonce` (raw bytes after base64url-decode) | `nonce` | Nitro's `nonce` field is designed for exactly this — an opaque per-attestation caller-supplied value that the NSM echoes into the signed document. Using it directly avoids inventing a synthetic packing convention. |
| `signing_key` (raw Ed25519 public-key bytes from JWK `x` after base64url-decode; 32 bytes) | `public_key` | Nitro's `public_key` field is documented as "the public key that the enclave wants to have attested." That is exactly what the binding rule anchors against. Raw 32-byte Ed25519 pubkey — the verifier reconstructs the JWK deterministically and compares thumbprints. |
| — | `user_data` | Unused for v1. Reserved so an extension can carry additional bound data (e.g., a workload identifier) without changing the wire shape. |

## What this means for the spec

The spec's normative page currently says (roughly):

> The JWK in `signing_key` MUST appear bound in the platform user-data slot of `attestation_document` alongside the nonce.

For Nitro, the more accurate normative claim is:

> The JWK in `signing_key` MUST appear bound in the `public_key` field of the Nitro attestation document; the envelope's `nonce` MUST byte-match the `nonce` field of the Nitro attestation document.

Two things follow:

1. **The "same slot" wording needs a per-format table.** Nitro splits nonce and public-key into separate fields. TDX and SEV-SNP have a single 64-byte `REPORTDATA`, which forces packing (nonce(32) ‖ thumbprint(32) is the natural fit). GCP Confidential Space is a JWT with `eat_nonce` and workload-image claims — no raw slot at all, so the projection is a claim-name mapping rather than a byte layout. **One normative registry entry per format** is what the spec needs.
2. **Thumbprint canonicalization stops being load-bearing on Nitro.** Because Nitro `public_key` carries the raw pubkey bytes, the "byte-match after canonical JWK serialization" wording collapses to "reconstruct the JWK from these bytes and check the thumbprint of the envelope's `signing_key` matches." The verifier still uses RFC 7638 thumbprints for the comparison, but the on-wire bytes are the raw key, not the JWK. This is a strictly cleaner story — worth calling out in the spec so implementers don't accidentally embed a JCS-encoded JWK where a raw pubkey is expected.

## What was tried and rejected

- **JWK thumbprint (32 bytes) in `user_data`, raw pubkey in `public_key`.** Redundant — the thumbprint is derivable from `public_key`.
- **JCS-encoded JWK bytes in `public_key`.** Fits (JCS Ed25519 JWK is ~150 bytes; Nitro `public_key` allows up to 1024), but conflates JWK-text canonicalization concerns into the platform binding. Raw bytes are simpler.
- **Nonce packed into `user_data` alongside the JWK.** Wastes Nitro's dedicated `nonce` field and forces a synthetic packing convention.

## Deferred

- **TDX/SEV-SNP `REPORTDATA` layout** — proposed nonce(32) ‖ SHA-256(canonical Ed25519 pubkey bytes)(32). Not implemented in this prototype; the finding here is Nitro-only.
- **GCP Confidential Space claim mapping** — needs research against the actual token schema. Bokelley's review flagged that `submods.container.image_digest` is the workload claim, not `submods.confidential_space.image_digest`. The nonce carrier is `eat_nonce`. The JWK binding needs a specific claim path (likely a Google-defined custom claim, or piggybacking on `submods.container.workload_labels`).
