#!/bin/bash
# Downloads AdCP JSON schemas from the published protocol bundle at the pinned version.
#
# Usage:
#   ./download.sh              # download at pinned VERSION
#   ./download.sh 3.1.0        # download a specific released version
#   ./download.sh latest       # download the dev snapshot
#
# The bundle is fetched from https://adcontextprotocol.org/protocol/{version}.tgz
# with a SHA-256 sidecar for integrity. No GitHub API token required.
#
# After downloading, regenerate Go types:
#   python3 generate.py > ../types_gen.go

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION_FILE="$SCRIPT_DIR/VERSION"
BASE="https://adcontextprotocol.org/protocol"

# Sigstore identity the upstream release workflow signs under.
# See adcontextprotocol/adcp#2273.
COSIGN_IDENTITY='^https://github\.com/adcontextprotocol/adcp/\.github/workflows/release\.yml@refs/heads/.*$'
COSIGN_ISSUER='https://token.actions.githubusercontent.com'

# Set ADCP_STRICT_VERIFY=1 to fail when signature sidecars are missing or when
# cosign is unavailable. Default behavior: verify when possible, fall back to
# checksum-only trust otherwise (expected for latest.tgz and older releases).
STRICT="${ADCP_STRICT_VERIFY:-0}"

# Files in $SCRIPT_DIR that sit alongside the schema tree and must survive
# the rsync --delete. Add to this list when introducing new sibling files.
PROTECTED=(
  VERSION
  .gitignore
  .bundle-sha256
  download.sh
  check-freshness.sh
  generate.py
)

VERSION="${1:-$(cat "$VERSION_FILE" | tr -d '[:space:]')}"

# Validate VERSION to keep it safe for URL + filesystem interpolation.
if ! [[ "$VERSION" =~ ^(latest|[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?)$ ]]; then
  echo "invalid version: '$VERSION' (expected 'latest' or semver like 3.1.0)" >&2
  exit 1
fi

echo "Downloading AdCP protocol bundle @ $VERSION"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

CURL_OPTS=(--fail --silent --show-error --location --retry 3 --retry-all-errors --max-time 60)
curl "${CURL_OPTS[@]}" -o "$WORK/bundle.tgz" "$BASE/$VERSION.tgz"
curl "${CURL_OPTS[@]}" -o "$WORK/bundle.tgz.sha256" "$BASE/$VERSION.tgz.sha256"

EXPECTED=$(cut -d' ' -f1 "$WORK/bundle.tgz.sha256")
if ! [[ "$EXPECTED" =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid sidecar hash: '$EXPECTED'" >&2
  exit 1
fi
ACTUAL=$(shasum -a 256 "$WORK/bundle.tgz" | cut -d' ' -f1)
if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "SHA-256 mismatch: expected $EXPECTED, got $ACTUAL" >&2
  exit 1
fi

# Optional Sigstore verification — proves the bundle was produced by the
# upstream release workflow, not just served from the same origin.
# latest.tgz and pre-signing releases do not ship sidecars; treat as
# checksum-only unless ADCP_STRICT_VERIFY=1 is set.
SIG_CODE=$(curl -sSL -o "$WORK/bundle.tgz.sig" -w "%{http_code}" "$BASE/$VERSION.tgz.sig" || echo 000)
CRT_CODE=$(curl -sSL -o "$WORK/bundle.tgz.crt" -w "%{http_code}" "$BASE/$VERSION.tgz.crt" || echo 000)
if [ "$SIG_CODE" = "200" ] && [ "$CRT_CODE" = "200" ]; then
  if command -v cosign >/dev/null 2>&1; then
    cosign verify-blob \
      --signature "$WORK/bundle.tgz.sig" \
      --certificate "$WORK/bundle.tgz.crt" \
      --certificate-identity-regexp "$COSIGN_IDENTITY" \
      --certificate-oidc-issuer "$COSIGN_ISSUER" \
      "$WORK/bundle.tgz"
    echo "Sigstore verification passed."
  elif [ "$STRICT" = "1" ]; then
    echo "cosign not installed but signature sidecars are available (ADCP_STRICT_VERIFY=1)" >&2
    exit 1
  else
    echo "cosign not installed — skipping signature verification (checksum-only)."
  fi
else
  rm -f "$WORK/bundle.tgz.sig" "$WORK/bundle.tgz.crt"
  if [ "$STRICT" = "1" ]; then
    echo "no signature sidecars for $VERSION (ADCP_STRICT_VERIFY=1)" >&2
    exit 1
  fi
  echo "No signature sidecars for $VERSION — checksum-only trust."
fi

# Reject archive entries that would escape the extraction directory before
# calling tar. The sha256 check proves the bundle matches upstream, but does
# not attest the bundle's contents are safe.
if tar tzf "$WORK/bundle.tgz" | grep -qE '(^/|(^|/)\.\.(/|$))'; then
  echo "bundle contains unsafe paths" >&2
  exit 1
fi

tar xzf "$WORK/bundle.tgz" -C "$WORK" --no-same-owner

# Reject bundles containing symlinks — they would be preserved by rsync -a
# and could point anywhere (incl. outside the repo) when later tools follow them.
if find "$WORK/adcp-$VERSION" -type l -print -quit | grep -q .; then
  echo "bundle contains symlinks — refusing to extract" >&2
  exit 1
fi

SRC="$WORK/adcp-$VERSION/schemas"
if [ ! -d "$SRC" ]; then
  echo "bundle layout unexpected: missing $SRC" >&2
  ls "$WORK" >&2
  exit 1
fi

# Preserve the scripts and sidecar files that live alongside the schema tree.
EXCLUDE_ARGS=()
for f in "${PROTECTED[@]}"; do
  EXCLUDE_ARGS+=(--exclude "$f")
done
rsync -a --delete "${EXCLUDE_ARGS[@]}" "$SRC/" "$SCRIPT_DIR/"

# Record the bundle's SHA-256 so check-freshness.sh can detect drift against
# the upstream artifact (meaningful for the "latest" dev snapshot).
echo "$EXPECTED" > "$SCRIPT_DIR/.bundle-sha256"

# Update VERSION file if a specific version was passed
if [ -n "${1:-}" ]; then
  echo "$1" > "$VERSION_FILE"
  echo "Updated VERSION to $1"
fi

echo "Done. Run 'python3 generate.py > ../types_gen.go' to regenerate Go types."
