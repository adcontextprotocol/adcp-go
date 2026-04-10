#!/bin/bash
# Downloads AdCP JSON schemas from the spec repo at the pinned version.
#
# Usage:
#   ./download.sh              # download at pinned VERSION
#   ./download.sh v3.0.0       # download at specific version
#   ./download.sh main         # download from main branch
#
# After downloading, regenerate Go types:
#   python3 generate.py > ../types_gen.go

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION_FILE="$SCRIPT_DIR/VERSION"

# Use argument, or fall back to VERSION file
REF="${1:-$(cat "$VERSION_FILE" | tr -d '[:space:]')}"
REPO="adcontextprotocol/adcp"

echo "Downloading schemas from $REPO @ $REF"

# Get all schema file paths
PATHS=$(gh api -X GET "repos/$REPO/git/trees/$REF?recursive=1" \
  --jq '.tree[].path' | grep '^static/schemas/source/.*\.json$')

COUNT=$(echo "$PATHS" | wc -l | tr -d ' ')
echo "Found $COUNT schema files"

# Download each file
echo "$PATHS" | while IFS= read -r path; do
  rel="${path#static/schemas/source/}"
  dir="$(dirname "$rel")"
  mkdir -p "$SCRIPT_DIR/$dir"
  gh api "repos/$REPO/contents/$path?ref=$REF" \
    -H 'Accept: application/vnd.github.raw' \
    > "$SCRIPT_DIR/$rel" 2>/dev/null
done

# Update VERSION file if a specific version was passed
if [ -n "${1:-}" ]; then
  echo "$1" > "$VERSION_FILE"
  echo "Updated VERSION to $1"
fi

echo "Done. Run 'python3 generate.py > ../types_gen.go' to regenerate Go types."
