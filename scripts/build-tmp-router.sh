#!/usr/bin/env bash
#
# Reproducible build of the TMP router OCI image.
#
# This script produces the same image digest as the CI workflow at
# .github/workflows/tmp-router-image.yml given the same source tree and the
# same Docker / BuildKit version. Use it to:
#
#   - rebuild a release locally and confirm the digest matches what was
#     published (auditor / verifier-side reproducibility check)
#   - produce the digest a TEE attestation verifier needs to allowlist
#
# Usage:
#   scripts/build-tmp-router.sh [--platform <plat>] [--load|--push <tag>] [--measurements-out <path>]
#
# Examples:
#   scripts/build-tmp-router.sh                              # build linux/amd64 to BuildKit cache, print digest
#   scripts/build-tmp-router.sh --platform linux/arm64       # arm64 instead
#   scripts/build-tmp-router.sh --measurements-out out.json  # also write the measurements manifest
#
# Requirements: Docker 24+ with BuildKit, jq (for --measurements-out).
#
# SOURCE_DATE_EPOCH is derived from the last commit that touched the build
# inputs (Dockerfile, Go sources under router/, cmd/router/, tmproto/,
# targeting/, urlcanon/, plus go.mod/go.sum). Pass SOURCE_DATE_EPOCH=<unix-ts>
# in the environment to override.

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PLATFORM="linux/amd64"
ACTION=""                 # one of: "" (build only, no output), "load", "push"
PUSH_TAG=""
MEASUREMENTS_OUT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --platform)
      PLATFORM="$2"; shift 2;;
    --load)
      ACTION="load"; shift;;
    --push)
      ACTION="push"; PUSH_TAG="$2"; shift 2;;
    --measurements-out)
      MEASUREMENTS_OUT="$2"; shift 2;;
    -h|--help)
      sed -n '2,/^set -euo pipefail/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0;;
    *)
      echo "unknown flag: $1" >&2; exit 2;;
  esac
done

if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
  SOURCE_DATE_EPOCH="$(git log -1 --format=%ct -- \
    cmd/router/Dockerfile cmd/router/ router/ tmproto/ targeting/ urlcanon/ \
    go.mod go.sum 2>/dev/null || echo 0)"
fi
export SOURCE_DATE_EPOCH

echo "==> TMP router reproducible build" >&2
echo "    SOURCE_DATE_EPOCH = $SOURCE_DATE_EPOCH ($(date -u -r "$SOURCE_DATE_EPOCH" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo unknown))" >&2
echo "    platform          = $PLATFORM" >&2

METADATA_FILE="$(mktemp -t tmp-router-metadata.XXXXXX.json)"
trap 'rm -f "$METADATA_FILE"' EXIT

OUTPUT_FLAGS=()
case "$ACTION" in
  load)
    OUTPUT_FLAGS=(--load);;
  push)
    if [[ -z "$PUSH_TAG" ]]; then echo "--push requires a tag argument" >&2; exit 2; fi
    OUTPUT_FLAGS=(--push --tag "$PUSH_TAG");;
  "")
    OUTPUT_FLAGS=(--output=type=image,push=false);;
esac

docker buildx build \
  --file cmd/router/Dockerfile \
  --platform "$PLATFORM" \
  --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
  --provenance=false \
  --sbom=false \
  --metadata-file "$METADATA_FILE" \
  "${OUTPUT_FLAGS[@]}" \
  .

DIGEST="$(jq -r '."containerimage.digest" // empty' "$METADATA_FILE")"
if [[ -z "$DIGEST" ]]; then
  echo "build succeeded but BuildKit did not report a digest (metadata: $(cat "$METADATA_FILE"))" >&2
  exit 1
fi

echo "==> image digest: $DIGEST" >&2
echo "$DIGEST"

if [[ -n "$MEASUREMENTS_OUT" ]]; then
  SOURCE_REV="$(git rev-parse --verify HEAD 2>/dev/null || echo unknown)"
  SOURCE_REV_SHORT="$(git rev-parse --short --verify HEAD 2>/dev/null || echo unknown)"
  SOURCE_DIRTY="false"
  if ! git diff --quiet HEAD -- 2>/dev/null; then SOURCE_DIRTY="true"; fi
  # Schema matches .github/workflows/tmp-router-image.yml — `platform_digests`
  # is a {<platform>: <digest>} map (the local build records only the one
  # platform it produced); `index_digest` is omitted since a local build does
  # not produce a multi-arch index. `source.dirty` is local-only.
  jq -n \
    --arg digest "$DIGEST" \
    --arg platform "$PLATFORM" \
    --arg source_date_epoch "$SOURCE_DATE_EPOCH" \
    --arg source_rev "$SOURCE_REV" \
    --arg source_rev_short "$SOURCE_REV_SHORT" \
    --argjson source_dirty "$SOURCE_DIRTY" \
    '{
      schema: "tmp-router-measurements/v1",
      platform_digests: { ($platform): $digest },
      source: {
        revision: $source_rev,
        revision_short: $source_rev_short,
        dirty: $source_dirty,
        date_epoch: ($source_date_epoch | tonumber)
      },
      reproducibility: {
        note: "Local single-platform reproducible build. Compare `platform_digests.\"\($platform)\"` to the matching entry under `platform_digests` in the CI-published manifest at the same revision. The CI manifest also records an `index_digest`; that is NOT the value an auditor or a TEE attestation verifier compares — index digests change with provenance/SBOM attestation manifests. See docs/tmp-router-reproducible-build.md."
      }
    }' > "$MEASUREMENTS_OUT"
  echo "==> measurements manifest written to: $MEASUREMENTS_OUT" >&2
fi
