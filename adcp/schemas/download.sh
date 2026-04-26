#!/bin/bash
# Downloads AdCP JSON schemas from the published protocol bundle at the pinned version.
#
# Usage:
#   ./download.sh              # download at pinned VERSION
#   ./download.sh 3.1.0        # download a specific released version
#   ./download.sh latest       # download the dev snapshot
#
# The bundle is fetched from https://adcontextprotocol.org/protocol/{version}.tgz
# with a SHA-256 sidecar for integrity and (for released versions) a Sigstore
# signature that proves the bundle came from the upstream release workflow.
#
# Trust model:
#   - Released versions (e.g. 3.1.0): signature verification is required.
#     cosign must be on PATH; fails closed if sidecars or cosign are missing.
#   - latest (dev snapshot): unsigned by design. Verified by SHA-256 only.
#
# Set ADCP_STRICT_VERIFY=1 to require signatures for latest too (useful if
# upstream opts in to signing latest.tgz).
#
# After downloading, regenerate Go types:
#   python3 generate.py > ../types_gen.go

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION_FILE="$SCRIPT_DIR/VERSION"
BASE="https://adcontextprotocol.org/protocol"

# Sigstore verification policy — pinned in-tree deliberately. Sourcing these
# from the /protocol/ manifest would let an attacker with origin write access
# (same host that serves the tarball + sidecars) swap the identity regex to
# match whatever cert they've obtained. Hardcoding means origin compromise
# alone is insufficient — an attacker would also need to run a workflow from
# adcontextprotocol/adcp that passes this exact identity match.
COSIGN_IDENTITY='^https://github\.com/adcontextprotocol/adcp/\.github/workflows/release\.yml@refs/(heads|tags)/.*$'
COSIGN_ISSUER='https://token.actions.githubusercontent.com'

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
  lint.py
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

# Signature verification. Released versions must be signed; latest is unsigned
# by design (rebuilt continuously — signatures would go stale immediately).
SIG_REQUIRED=1
if [ "$VERSION" = "latest" ] && [ "$STRICT" != "1" ]; then
  SIG_REQUIRED=0
fi

SIG_CODE=$(curl -sSL -o "$WORK/bundle.tgz.sig" -w "%{http_code}" "$BASE/$VERSION.tgz.sig" || echo 000)
[ "$SIG_CODE" = "200" ] || rm -f "$WORK/bundle.tgz.sig"
CRT_CODE=$(curl -sSL -o "$WORK/bundle.tgz.crt" -w "%{http_code}" "$BASE/$VERSION.tgz.crt" || echo 000)
[ "$CRT_CODE" = "200" ] || rm -f "$WORK/bundle.tgz.crt"

if [ "$SIG_CODE" = "200" ] && [ "$CRT_CODE" = "200" ]; then
  # A CDN returning 200 with an HTML error body would otherwise produce an
  # opaque cosign unmarshal error. Fail fast with a clear message.
  if ! grep -q "BEGIN CERTIFICATE" "$WORK/bundle.tgz.crt"; then
    echo "signature certificate for $VERSION is not a PEM certificate (CDN misbehaving?)" >&2
    exit 1
  fi
  if ! command -v cosign >/dev/null 2>&1; then
    echo "cosign not installed but signature sidecars are available for $VERSION" >&2
    echo "install cosign (https://docs.sigstore.dev/system_config/installation/) to verify" >&2
    exit 1
  fi
  cosign verify-blob \
    --signature "$WORK/bundle.tgz.sig" \
    --certificate "$WORK/bundle.tgz.crt" \
    --certificate-identity-regexp "$COSIGN_IDENTITY" \
    --certificate-oidc-issuer "$COSIGN_ISSUER" \
    "$WORK/bundle.tgz"
  echo "Sigstore verification passed."
else
  if [ "$SIG_REQUIRED" = "1" ]; then
    echo "no signature sidecars for $VERSION — released bundles must be signed" >&2
    exit 1
  fi
  echo "No signature sidecars for $VERSION — checksum-only trust (dev snapshot)."
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

# Manifest-driven sync of protocol-managed agent skills.
#
# Bundles starting with adcp#3097 enumerate canonical skills under
# `manifest.contents.skills`. Each named directory holds a `SKILL.md` (the
# wire contract for buyers/agents) and may bundle a `schemas/` subdir that
# duplicates the top-level schemas — verified identical at sync-time, so we
# filter it out (the SDK already has those in its schema cache).
#
# Older bundles without a skills entry no-op gracefully.
MANIFEST="$WORK/adcp-$VERSION/manifest.json"
SKILLS_REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)/skills"
if [ -f "$MANIFEST" ]; then
  # Validate skill names against ^[a-zA-Z0-9_-]+$ before any filesystem use —
  # this is a separate check from tar's path-traversal guard above and must
  # fail closed. Names are interpolated into mkdir/rsync paths below. Write
  # to a tempfile and check the python exit code explicitly, since command
  # substitution swallows non-zero exits under `set -e`.
  SKILL_LIST="$WORK/skill-names.txt"
  python3 - "$MANIFEST" >"$SKILL_LIST" <<'PY'
import json, re, sys
manifest = json.load(open(sys.argv[1]))
names = manifest.get("contents", {}).get("skills")
if not isinstance(names, list):
    sys.exit(0)
pattern = re.compile(r"^[a-zA-Z0-9_-]+$")
for n in names:
    if not isinstance(n, str) or not pattern.match(n):
        sys.stderr.write(f"invalid skill name in manifest: {n!r}\n")
        sys.exit(1)
print("\n".join(names))
PY
  if [ -s "$SKILL_LIST" ]; then
    mkdir -p "$SKILLS_REPO_DIR"
    while IFS= read -r SKILL_NAME; do
      [ -z "$SKILL_NAME" ] && continue
      SRC_SKILL="$WORK/adcp-$VERSION/skills/$SKILL_NAME"
      DST_SKILL="$SKILLS_REPO_DIR/$SKILL_NAME"
      if [ ! -d "$SRC_SKILL" ]; then
        echo "manifest names skill '$SKILL_NAME' but bundle has no $SRC_SKILL" >&2
        exit 1
      fi
      mkdir -p "$DST_SKILL"
      # Exclude the per-skill schemas/ subdir at the skill root. These are
      # byte-identical copies of the top-level schemas already in the SDK's
      # schema cache; including them would duplicate ~1.4MB per protocol.
      rsync -a --delete --exclude='/schemas/' "$SRC_SKILL/" "$DST_SKILL/"
      echo "Synced protocol skill: $SKILL_NAME"
    done < "$SKILL_LIST"
  fi
fi

# Update VERSION file if a specific version was passed
if [ -n "${1:-}" ]; then
  echo "$1" > "$VERSION_FILE"
  echo "Updated VERSION to $1"
fi

echo "Done. Run 'python3 generate.py > ../types_gen.go' to regenerate Go types."
