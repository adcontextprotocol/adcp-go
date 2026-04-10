#!/bin/bash
# Checks whether the pinned schema version matches the latest AdCP release or RC.
# Returns exit code 0 if up to date, 1 if stale.
#
# Usage:
#   ./check-freshness.sh          # check against latest tag
#   ./check-freshness.sh --rc     # check against latest RC tag
#
# Intended for CI: run on a schedule or as a PR check.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION_FILE="$SCRIPT_DIR/VERSION"
PINNED=$(cat "$VERSION_FILE" | tr -d '[:space:]')
REPO="adcontextprotocol/adcp"

if [ "${1:-}" = "--rc" ]; then
  # Get latest tag (including RCs)
  LATEST=$(gh api "repos/$REPO/tags" --jq '.[0].name')
else
  # Get latest stable release
  LATEST=$(gh api "repos/$REPO/releases/latest" --jq '.tag_name' 2>/dev/null || echo "none")
  if [ "$LATEST" = "none" ]; then
    # No stable release — fall back to latest tag
    LATEST=$(gh api "repos/$REPO/tags" --jq '.[0].name')
  fi
fi

echo "Pinned:  $PINNED"
echo "Latest:  $LATEST"

if [ "$PINNED" = "$LATEST" ]; then
  echo "Up to date."
  exit 0
else
  echo "Stale. Run: cd adcp/schemas && ./download.sh $LATEST && python3 generate.py > ../types_gen.go"
  exit 1
fi
