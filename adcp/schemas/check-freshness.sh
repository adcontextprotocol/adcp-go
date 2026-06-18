#!/bin/bash
# Checks whether the pinned schema bundle is up to date against the published
# artifact at https://adcontextprotocol.org/protocol/{VERSION}.tgz.
# Returns exit code 0 if up to date, 1 if stale.
#
# Usage:
#   ./check-freshness.sh
#
# For VERSION=latest (dev snapshot), freshness is determined by comparing the
# pinned bundle's SHA-256 against the current upstream sidecar. For pinned
# releases (e.g. 3.1.0), the bundle is immutable — freshness is determined by
# checking whether a newer release is listed in the /protocol/ manifest.
#
# Intended for CI: run on a schedule or as a PR check.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION_FILE="$SCRIPT_DIR/VERSION"
HASH_FILE="$SCRIPT_DIR/.bundle-sha256"
PINNED=$(cat "$VERSION_FILE" | tr -d '[:space:]')
BASE="https://adcontextprotocol.org/protocol"

# Run the drift linter regardless of freshness outcome so a stale bundle +
# drift surface together in one run, rather than forcing two fix loops.
run_lint() {
  if ! python3 "$SCRIPT_DIR/lint.py" --allow-missing-schemas; then
    echo "Schema drift detected. See adcp/schemas/lint.py output above for remediation." >&2
    return 1
  fi
  return 0
}

if ! [[ "$PINNED" =~ ^(latest|[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?)$ ]]; then
  echo "invalid VERSION: '$PINNED'" >&2
  exit 2
fi

CURL_OPTS=(--fail --silent --show-error --location --retry 3 --retry-all-errors --max-time 30)

if [ "$PINNED" = "latest" ]; then
  UPSTREAM=$(curl "${CURL_OPTS[@]}" "$BASE/latest.tgz.sha256" | cut -d' ' -f1)
  if ! [[ "$UPSTREAM" =~ ^[0-9a-f]{64}$ ]]; then
    echo "could not read upstream sha256" >&2
    exit 2
  fi
  PINNED_HASH=""
  if [ -f "$HASH_FILE" ]; then
    PINNED_HASH=$(cat "$HASH_FILE" | tr -d '[:space:]')
  fi
  echo "Pinned:  latest @ ${PINNED_HASH:-unknown}"
  echo "Latest:  latest @ $UPSTREAM"
  stale=0
  if [ "$PINNED_HASH" = "$UPSTREAM" ]; then
    echo "Up to date."
  else
    echo "Stale. Run: cd adcp/schemas && ./download.sh && python3 generate.py > ../types_gen.go"
    stale=1
  fi
  lint_rc=0
  run_lint || lint_rc=$?
  [ "$stale" -eq 0 ] && [ "$lint_rc" -eq 0 ] && exit 0
  exit 1
fi

# Pinned release: check the manifest for newer released versions.
MANIFEST=$(curl "${CURL_OPTS[@]}" "$BASE/")
LATEST=$(python3 -c '
import json, sys, re
m = json.load(sys.stdin)
versions = m.get("versions") or []
def version_value(entry):
    if isinstance(entry, str):
        return entry
    if isinstance(entry, dict):
        v = entry.get("version")
        if isinstance(v, str):
            return v
    return ""
def key(v):
    v = v.lstrip("v")
    parts = v.split("-", 1)
    nums = tuple(int(x) for x in parts[0].split(".") if x.isdigit())
    pre = parts[1] if len(parts) > 1 else ""
    return (nums, pre == "", pre)
released = [v for v in (version_value(entry) for entry in versions) if re.match(r"^v?\d+\.\d+\.\d+$", v)]
released.sort(key=key)
print(released[-1] if released else "")
' <<<"$MANIFEST")

echo "Pinned:  $PINNED"
echo "Latest:  ${LATEST:-<no released versions>}"

stale=0
if [ -z "$LATEST" ] || [ "$PINNED" = "$LATEST" ]; then
  echo "Up to date."
else
  echo "Stale. Run: cd adcp/schemas && ./download.sh $LATEST && python3 generate.py > ../types_gen.go"
  stale=1
fi
lint_rc=0
run_lint || lint_rc=$?
[ "$stale" -eq 0 ] && [ "$lint_rc" -eq 0 ] && exit 0
exit 1
