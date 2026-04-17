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
