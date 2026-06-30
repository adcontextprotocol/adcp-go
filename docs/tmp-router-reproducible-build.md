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

Multi-platform builds (`linux/amd64`, `linux/arm64`) are deterministic per platform. The CI workflow pushes a multi-arch *index* that references per-platform image manifests **plus** provenance and SBOM attestation manifests; the per-platform image manifests are what an auditor reproduces and what a TEE attestation chain binds against. The index digest itself is not reproducible by a local single-platform build (and not expected to be) — the relevant comparison is always at the per-platform level.

## Verifying a published image

Anyone — auditor, CISO, regulator, paranoid operator — can verify reproducibility with:

```bash
# 1. Clone at the same revision as the published image.
git clone https://github.com/adcontextprotocol/adcp-go
cd adcp-go
git checkout <commit-or-tag>

# 2. Rebuild locally for the platform you want to verify. The script prints
#    the platform-specific image-manifest digest.
scripts/build-tmp-router.sh --platform linux/amd64

# 3. Read the matching per-platform digest from the published manifest list.
#    The registry publishes a multi-arch INDEX; the index references one
#    image-manifest per platform plus provenance/SBOM attestation manifests.
#    What you compare against is the per-platform image manifest, NOT the
#    index digest — the index hash changes with provenance/SBOM by design.
docker buildx imagetools inspect \
  ghcr.io/adcontextprotocol/adcp-go/tmp-router:<tag> \
  --raw \
| jq -r '.manifests[]
    | select(.platform.architecture == "amd64" and .platform.os == "linux"
             and (.annotations["vnd.docker.reference.type"] | not))
    | .digest'
```

The two digests must be identical. If they are not, do not allowlist the published image — open an issue and treat the divergence as a build-pipeline integrity incident until explained.

The `platform_digests` map in the CI-published `tmp-router-measurements.json` is the authoritative shortcut — it records the same per-platform digests, so an auditor with the manifest in hand can skip the `imagetools inspect | jq` step and compare against the manifest entry directly.

The Sigstore signature on the published image is independent of reproducibility — it tells you "GitHub Actions for this repo built and signed this digest." Reproducibility tells you "this source tree produces this digest." Both are needed: signature without reproducibility means a malicious workflow could publish a backdoored binary; reproducibility without signature means anyone could publish look-alike binaries.

## The measurements manifest

Every CI build produces a `tmp-router-measurements.json` artifact with this shape:

```json
{
  "schema": "tmp-router-measurements/v1",
  "image": "ghcr.io/adcontextprotocol/adcp-go/tmp-router",
  "index_digest": "sha256:...",
  "platform_digests": {
    "linux/amd64": "sha256:...",
    "linux/arm64": "sha256:..."
  },
  "source": {
    "revision": "<git sha>",
    "revision_short": "<short sha>",
    "ref": "refs/tags/tmp-router-v0.1.0",
    "date_epoch": 1782825869
  },
  "build": { "workflow_run": "...", "runner": "github-hosted ubuntu-latest" },
  "reproducibility": { "note": "..." }
}
```

`platform_digests` is the value a verifier or auditor compares against. `index_digest` is the multi-arch index pushed to the registry (the same digest cosign signs) and is recorded for traceability, but it includes the `provenance: mode=max` and SBOM attestation manifests — so it is not byte-stable across CI runs that recompute provenance, and it is not what a TEE attestation chain binds against. Local reproducible builds (single-platform) cannot reproduce an index digest by construction; they reproduce a per-platform image-manifest digest, which is what `platform_digests` records.

The local script's manifest shares the same schema (`tmp-router-measurements/v1`) with `platform_digests` carrying the one platform built, and `index_digest` omitted; an additional local-only `source.dirty` flag is included so an auditor's running build state is visible. A schema validator that wants strict cross-emitter compatibility should treat both `index_digest` and `source.dirty` as optional.

For tag pushes the CI manifest is also attached to the published image as a Sigstore attestation (`cosign attest --type custom`). Verifiers retrieve it from the registry with:

```bash
cosign verify-attestation --type custom \
  ghcr.io/adcontextprotocol/adcp-go/tmp-router@sha256:<digest> \
  --certificate-identity-regexp='https://github.com/adcontextprotocol/adcp-go/.github/workflows/tmp-router-image\.yml@.*' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com'
```

For non-tag pushes the manifest is only available as a workflow-run artifact.

## How per-platform digests map to TEE measurements

A per-platform image-manifest digest from `platform_digests` is the *workload identity* the verifier needs, but the format-specific measurement value differs:

| TEE format | Measurement | Relation to a `platform_digests` entry |
|---|---|---|
| GCP Confidential Space | `submods.container.image_digest` claim in the attestation token (the `container` submodule carries workload-image identity; `confidential_space` carries platform/support attributes). | **Direct equality** — the verifier compares the token's `submods.container.image_digest` against the `platform_digests` entry for the platform the workload runs on (a Confidential Space VM runs a single platform image, not a multi-arch index, so the comparison is per-platform). |
| AWS Nitro Enclaves | PCR0/PCR1/PCR2 in the attestation document | **Derived deterministically** from the OCI image plus the Nitro CLI version *and* the linuxkit kernel/init blobs that ship with that Nitro CLI release — bumping the Nitro CLI base layer changes PCR0 even when the OCI image is unchanged. Build the EIF with `nitro-cli build-enclave --docker-uri <image>@<per-platform-digest>` on a Nitro-enabled host; the PCR values fall out of the build. The published `tmp-router-measurements.json` declares the OCI per-platform digest; the operator's Nitro-build step produces the EIF measurement separately, pinned to a specific Nitro CLI version. |
| Intel TDX | MRTD in the quote | Same model as Nitro: derived from the image plus the TDX measurement-build chain. |
| AMD SEV-SNP | SNP_MEASUREMENT in the report | Same model: image-plus-host-chain derivation. |

In every non-GCP case, the operator runs a one-shot transformation from the per-platform OCI image to the platform-specific measurement on a host with the platform tooling. That transformation is itself deterministic — same per-platform image plus same tool version equals same measurement value — but it is *not run in this CI* because GitHub-hosted runners don't have Nitro / TDX / SEV-SNP tooling. Each platform's measurement value should be derived once per release on a controlled host and published alongside the AdCP `tmp-router-measurements.json` manifest.

## What this does NOT cover

- **Per-platform PCR / MRTD / SNP_MEASUREMENT publication.** Build-side responsibility is up to and including the OCI image digest. The platform-specific measurement is derived elsewhere (see table above). A follow-up will add the operator-side procedure for publishing those per-platform values once we have at least one Nitro deployment in the loop.
- **Transparency-log of build provenance.** The Sigstore signature and the in-toto provenance (`provenance: mode=max` in the workflow) cover this for the OCI layer; we have not yet hooked a separate Rekor-style measurement registry.
- **Reproducibility of dependencies the Go toolchain does not pin.** `CGO_ENABLED=0` removes the cgo / libc concern; the Go module proxy plus `go.sum` checksum verification covers source deps; the Alpine base image is pinned by digest. Beyond these, nothing in the build pipeline reaches out to the network at build time.

## Reporting reproducibility failures

If `scripts/build-tmp-router.sh` produces a different digest than the published image at the same revision, the divergence is either a bug in this pipeline or a compromise in CI. Treat it as a security incident:

1. Capture the local build's `tmp-router-measurements.json`.
2. Open an issue tagged `security/build-integrity` with the local manifest, the published manifest, your platform/Docker versions, and any toolchain mismatches.
3. Do not allowlist the published digest in any TEE attestation policy until the divergence is explained.
