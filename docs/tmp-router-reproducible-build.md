# Reproducible build of the TMP router

This document explains how the TMP router (`cmd/router`) is built reproducibly, how to verify a published image matches the source, and how the resulting image digest relates to TEE attestation measurements. It is intended for operators, auditors, and verifiers running a TEE-attested TMP Router deployment.

The wire-protocol side of TEE attestation (the envelope, the binding rule, the `/.well-known/` endpoint) is specified separately — see [adcontextprotocol/adcp PR #5770](https://github.com/adcontextprotocol/adcp/pull/5770) and `docs/trusted-match/router-attestation.mdx` in the spec repo. This page only covers the *build* side: how we produce the binary whose measurement the wire spec lets a verifier check.

## Why reproducibility matters here

A TEE attestation document carries a cryptographic measurement of the running workload — for Nitro that's the PCR0 hash of the EIF; for Intel TDX it's MRTD inside the quote; for AMD SEV-SNP it's SNP_MEASUREMENT; for GCP Confidential Space the workload-image digest is one of the bound claims. A verifier compares that measurement against an *allowlist* of expected values. The allowlist is only as trustworthy as the procedure that produces the expected values.

If two operators build the same source tree and get different image digests, the allowlist mechanism breaks: you can never tell whether a divergent measurement is "a bug in the build" or "a backdoored binary." Reproducibility is the property that closes that loop — anyone can rebuild from source and confirm the published measurement.

## What's pinned

The Dockerfile at [`cmd/router/Dockerfile`](../cmd/router/Dockerfile) pins:

- **Base images by digest.** `golang:1.26-alpine` and `gcr.io/distroless/static-debian13:nonroot` are referenced by their multi-arch index digests. Renovate keeps the digests fresh; bumps land as commits and CI republishes a new measurement.
- **Go toolchain version** is pinned by the base image (`golang:1.26-alpine`).
- **Build flags.** `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and `-ldflags="-s -w -buildid="` strip all entropy from the produced binary — file paths, VCS metadata, Go's build-id, and the debug/symbol tables. Without these, the binary varies per build environment even with the same source.
- **`SOURCE_DATE_EPOCH`** is passed in as a build arg from the timestamp of the last commit that touched a build input. BuildKit uses it to normalize layer mtimes.

Multi-platform builds (`linux/amd64`, `linux/arm64`) are deterministic per platform — the published image is a multi-arch manifest pointing at platform-specific digests.

## Verifying a published image

Anyone — auditor, CISO, regulator, paranoid operator — can verify reproducibility with:

```bash
# 1. Clone at the same revision as the published image.
git clone https://github.com/adcontextprotocol/adcp-go
cd adcp-go
git checkout <commit-or-tag>

# 2. Rebuild locally. The script prints the OCI image digest.
scripts/build-tmp-router.sh --platform linux/amd64

# 3. Compare against the published digest.
docker buildx imagetools inspect ghcr.io/adcontextprotocol/adcp-go/tmp-router:<tag> \
  --format '{{.Manifest.Digest}}'
```

The two digests must be identical. If they are not, do not allowlist the published image — open an issue and treat the divergence as a build-pipeline integrity incident until explained.

The Sigstore signature on the published image is independent of reproducibility — it tells you "GitHub Actions for this repo built and signed this digest." Reproducibility tells you "this source tree produces this digest." Both are needed: signature without reproducibility means a malicious workflow could publish a backdoored binary; reproducibility without signature means anyone could publish look-alike binaries.

## The measurements manifest

Every CI build produces a `tmp-router-measurements.json` artifact with this shape:

```json
{
  "schema": "tmp-router-measurements/v1",
  "image": "ghcr.io/adcontextprotocol/adcp-go/tmp-router",
  "image_digest": "sha256:...",
  "platforms": ["linux/amd64", "linux/arm64"],
  "source": {
    "revision": "<git sha>",
    "revision_short": "<short sha>",
    "ref": "refs/tags/tmp-router-v0.1.0",
    "dirty": false,
    "date_epoch": 1782825869
  },
  "build": { "workflow_run": "...", "runner": "github-hosted ubuntu-latest" },
  "reproducibility": { "note": "..." }
}
```

For tag pushes the manifest is also attached to the published image as a Sigstore attestation (`cosign attest --type custom`). Verifiers retrieve it from the registry with:

```bash
cosign verify-attestation --type custom \
  ghcr.io/adcontextprotocol/adcp-go/tmp-router@sha256:<digest> \
  --certificate-identity-regexp='https://github.com/adcontextprotocol/adcp-go/.github/workflows/tmp-router-image\.yml@.*' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com'
```

For non-tag pushes the manifest is only available as a workflow-run artifact.

## How `image_digest` maps to platform measurements

The OCI image digest is the *workload identity* a TEE verifier needs, but the format-specific measurement value differs:

| TEE format | Measurement | Relation to `image_digest` |
|---|---|---|
| GCP Confidential Space | Workload-image digest in the token's `submods.confidential_space.image_digest` claim | **Direct equality** — the verifier compares `image_digest` to the token claim. |
| AWS Nitro Enclaves | PCR0/PCR1/PCR2 in the attestation document | **Derived deterministically** from the OCI image plus the Nitro CLI version. Build the EIF with `nitro-cli build-enclave --docker-uri <image>@<digest>` on a Nitro-enabled host; the PCR values fall out of the build. The published manifest declares the OCI digest; the operator's Nitro-build step produces the EIF measurement. |
| Intel TDX | MRTD in the quote | Same model as Nitro: derived from the image plus the TDX measurement-build chain. |
| AMD SEV-SNP | SNP_MEASUREMENT in the report | Same model: image-plus-host-chain derivation. |

In every non-GCP case, the operator runs a one-shot transformation from the OCI image to the platform-specific measurement on a host with the platform tooling. That transformation is itself deterministic — same OCI image plus same tool version equals same measurement value — but it is *not run in this CI* because GitHub-hosted runners don't have Nitro / TDX / SEV-SNP tooling. Each platform's measurement value should be derived once per release on a controlled host and published alongside the AdCP `tmp-router-measurements.json` manifest.

## What this does NOT cover

- **Per-platform PCR / MRTD / SNP_MEASUREMENT publication.** Build-side responsibility is up to and including the OCI image digest. The platform-specific measurement is derived elsewhere (see table above). A follow-up will add the operator-side procedure for publishing those per-platform values once we have at least one Nitro deployment in the loop.
- **Transparency-log of build provenance.** The Sigstore signature and the in-toto provenance (`provenance: mode=max` in the workflow) cover this for the OCI layer; we have not yet hooked a separate Rekor-style measurement registry.
- **Reproducibility of dependencies the Go toolchain does not pin.** `CGO_ENABLED=0` removes the cgo / libc concern; the Go module proxy plus `go.sum` checksum verification covers source deps; the Alpine base image is pinned by digest. Beyond these, nothing in the build pipeline reaches out to the network at build time.

## Reporting reproducibility failures

If `scripts/build-tmp-router.sh` produces a different digest than the published image at the same revision, the divergence is either a bug in this pipeline or a compromise in CI. Treat it as a security incident:

1. Capture the local build's `tmp-router-measurements.json`.
2. Open an issue tagged `security/build-integrity` with the local manifest, the published manifest, your platform/Docker versions, and any toolchain mismatches.
3. Do not allowlist the published digest in any TEE attestation policy until the divergence is explained.
