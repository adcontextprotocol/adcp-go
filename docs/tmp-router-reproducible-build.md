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

Anyone — auditor, CISO, regulator, paranoid operator — can verify reproducibility with the following procedure. The order matters: the cosign signature must be verified *before* any digest extracted from the registry is trusted, otherwise the procedure is anchoring trust in an unverified registry response.

```bash
# 0. Clone at the same revision as the published image.
git clone https://github.com/adcontextprotocol/adcp-go
cd adcp-go
git checkout <commit-or-tag>

# 1. Verify the cosign keyless signature on the published tag. This binds
#    trust to the GitHub Actions workflow that built and signed the image:
#    only signatures whose OIDC token names this exact workflow on `main`
#    or a `tmp-router-v*` tag are accepted. Capture the verified index
#    digest for the next step.
VERIFIED_INDEX_DIGEST="$(cosign verify \
  ghcr.io/adcontextprotocol/adcp-go/tmp-router:<tag> \
  --certificate-identity-regexp='^https://github\.com/adcontextprotocol/adcp-go/\.github/workflows/tmp-router-image\.yml@refs/(heads/main|tags/tmp-router-v[0-9].*)$' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com' \
  --output json \
  | jq -r '.[0].critical.image."docker-manifest-digest"')"
echo "verified index digest: $VERIFIED_INDEX_DIGEST"

# 2. Extract the per-platform image-manifest digest from the verified
#    index — NOT from the registry tag. The index references one
#    image-manifest per platform plus provenance/SBOM attestation
#    manifests; the filter on `vnd.docker.reference.type` excludes the
#    attestations and keeps the actual workload manifests. The digest
#    you read here inherits its trust from the cosign verify in step 1.
PUBLISHED_AMD64_DIGEST="$(docker buildx imagetools inspect \
  "ghcr.io/adcontextprotocol/adcp-go/tmp-router@${VERIFIED_INDEX_DIGEST}" \
  --raw \
| jq -r '.manifests[]
    | select(.platform.architecture == "amd64" and .platform.os == "linux"
             and (.annotations["vnd.docker.reference.type"] | not))
    | .digest')"
echo "published linux/amd64 digest: $PUBLISHED_AMD64_DIGEST"

# 3. Rebuild locally for the same platform. The script prints the per-platform
#    image-manifest digest as its last line of stdout.
LOCAL_AMD64_DIGEST="$(scripts/build-tmp-router.sh --platform linux/amd64 | tail -n1)"
echo "local rebuild  linux/amd64 digest: $LOCAL_AMD64_DIGEST"

# 4. Compare.
test "$PUBLISHED_AMD64_DIGEST" = "$LOCAL_AMD64_DIGEST" \
  && echo "OK — reproducibility verified." \
  || { echo "DIVERGED — do not allowlist this image." >&2; exit 1; }
```

The two digests must be identical. If they are not, do not allowlist the published image — open an issue and treat the divergence as a build-pipeline integrity incident until explained.

The `platform_digests` map in the published `tmp-router-measurements.json` records the same per-platform digests for convenience, but it is **not** a substitute for the rebuild — the cosign attestation that carries the manifest proves the workflow produced those values, not that an independent build reproduces them. CI itself performs an independent no-cache rebuild before signing (`Verify reproducibility (no-cache clean rebuild)` in the workflow), but operators with a higher bar than "trust our CI" run the procedure above themselves.

The Sigstore signature on the published image is independent of reproducibility — it tells you "this GitHub Actions workflow built and signed this digest." Reproducibility tells you "this source tree produces this digest." Both are needed: signature without reproducibility means a malicious workflow could publish a backdoored binary; reproducibility without signature means anyone could publish look-alike binaries.

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

For every push (both `main` and `tmp-router-v*` tags) the CI manifest is also attached to the published image as a Sigstore attestation under the stable predicate type `https://adcontextprotocol.org/tmp-router-measurements/v1`. Verifiers retrieve it from the registry with:

```bash
cosign verify-attestation \
  --type 'https://adcontextprotocol.org/tmp-router-measurements/v1' \
  ghcr.io/adcontextprotocol/adcp-go/tmp-router@sha256:<digest> \
  --certificate-identity-regexp='^https://github\.com/adcontextprotocol/adcp-go/\.github/workflows/tmp-router-image\.yml@refs/(heads/main|tags/tmp-router-v[0-9].*)$' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com'
```

The `--type` URI pins verification to the schema this workflow produces — `--type custom` would match any custom predicate attached to the image.

## How per-platform digests map to TEE measurements

A per-platform image-manifest digest from `platform_digests` is the *workload identity* the verifier needs, but the format-specific measurement value differs:

| TEE format | Headline measurement | What the measurement actually covers, and how to bind it to `platform_digests` |
|---|---|---|
| GCP Confidential Space | `submods.container.image_digest` claim in the attestation token (the `container` submodule carries workload-image identity; `confidential_space` carries platform/support attributes). | **Direct equality**, with a launch-reference caveat. The token's `submods.container.image_digest` compares byte-for-byte against the `platform_digests` entry for the platform the workload runs on (a Confidential Space VM runs a single platform image, not a multi-arch index, so the comparison is per-platform). The caveat: this only holds if the deployment is launched by the per-platform manifest digest, i.e. `tmp-router@<linux/amd64 digest>`. Launch by tag or by the multi-arch index digest and `submods.container.image_digest` may carry the index digest — which the reproducibility procedure never treats as trusted. **Operator MUST launch the workload as `tmp-router@<per-platform digest>`**, sourced from the `platform_digests` entry validated in the verification procedure above. |
| AWS Nitro Enclaves | PCR0 (matches EIF image), plus PCR1/PCR2 | **Full workload measurement.** PCR0 covers the entire EIF — kernel + init + application binary — so pinning it pins the tmp-router binary. Derived deterministically from the OCI image plus the Nitro CLI version *and* the linuxkit kernel/init blobs that ship with that Nitro CLI release — bumping the Nitro CLI base layer changes PCR0 even when the OCI image is unchanged. Build the EIF with `nitro-cli build-enclave --docker-uri <image>@<per-platform-digest>` on a Nitro-enabled host; the PCR values fall out of the build. The published `tmp-router-measurements.json` declares the OCI per-platform digest; the operator's Nitro-build step produces the EIF measurement separately, pinned to a specific Nitro CLI version. |
| Intel TDX | MRTD in the quote | **Partial — MRTD alone is not a workload measurement.** MRTD covers the TD's initial guest memory (TDVF firmware plus the pages loaded at TD build time) — the boot chain, not the running workload. The tmp-router binary lands in the guest via the boot chain and is measured into an **RTMR** (Runtime Measurement Register, typically RTMR3) by an IMA-style measured-boot chain, not into MRTD. An operator who allowlists an MRTD believing it identifies the tmp-router binary pins only the firmware/boot chain — a strictly weaker guarantee than PCR0. Deploying tmp-router on TDX with a meaningful workload allowlist requires (i) publishing the expected RTMR value(s) for the built rootfs/image alongside the per-platform digest, and (ii) verifying both MRTD (firmware) and the relevant RTMR (workload) in the attestation policy. |
| AMD SEV-SNP | LAUNCH_MEASUREMENT / SNP_MEASUREMENT in the attestation report | **Partial — SNP_MEASUREMENT alone is not a workload measurement.** Same shape as TDX MRTD: SNP_MEASUREMENT is the launch digest of the initial guest memory (OVMF firmware + boot pages), not the workload. Workload identity is anchored separately — most commonly a dm-verity roothash embedded in a measured kernel command line, or a measured initrd that dm-verity-mounts the rootfs. An operator who allowlists SNP_MEASUREMENT believing it pins the tmp-router binary pins only the firmware/boot chain. Deploying tmp-router on SEV-SNP with a meaningful workload allowlist requires publishing the expected dm-verity roothash (or equivalent workload anchor) alongside the per-platform digest, and verifying both SNP_MEASUREMENT (firmware) and the workload anchor in the attestation policy. |

**Bottom line for allowlist policy.** For GCP Confidential Space and AWS Nitro Enclaves, the headline measurement pins the tmp-router binary and a `platform_digests` entry (or a derived PCR0) is sufficient. For Intel TDX and AMD SEV-SNP, the headline register measures the VM boot chain only — the OCI image the operator built is the identity of the *guest workload*, but the guest workload measurement lives in a *separate* register (RTMR / dm-verity roothash) that this CI does not produce. TDX / SEV-SNP operators need a companion workload-anchor derivation step, out of scope for this PR.

For Nitro, TDX, and SEV-SNP the platform-specific measurement is derived once per release on a controlled host with the platform tooling — GitHub-hosted runners don't have Nitro / TDX / SEV-SNP CLIs. That derivation step is deterministic given a fixed per-platform image and fixed tool versions; the outputs should be published alongside the AdCP `tmp-router-measurements.json` manifest.

## What this does NOT cover

- **Per-platform PCR / MRTD / SNP_MEASUREMENT publication.** Build-side responsibility is up to and including the OCI image digest. The platform-specific measurement is derived elsewhere (see table above). A follow-up will add the operator-side procedure for publishing those per-platform values once we have at least one Nitro deployment in the loop.
- **Transparency-log of build provenance.** The Sigstore signature and the in-toto provenance (`provenance: mode=max` in the workflow) cover this for the OCI layer; we have not yet hooked a separate Rekor-style measurement registry.
- **Reproducibility of dependencies the Go toolchain does not pin.** `CGO_ENABLED=0` removes the cgo / libc concern; module downloads (via `GOPROXY`, which fetches from the Go module ecosystem during `go build`) are checksum-pinned by `go.sum` so the download source doesn't affect the produced bytes; the Alpine base image is pinned by digest.

## Reporting reproducibility failures

If `scripts/build-tmp-router.sh` produces a different digest than the published image at the same revision, the divergence is either a bug in this pipeline or a compromise in CI. Treat it as a security incident:

1. Capture the local build's `tmp-router-measurements.json`.
2. Open an issue tagged `security/build-integrity` with the local manifest, the published manifest, your platform/Docker versions, and any toolchain mismatches.
3. Do not allowlist the published digest in any TEE attestation policy until the divergence is explained.
