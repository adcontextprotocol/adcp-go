#!/usr/bin/env bash
# Fails if go.work.example's use () block drifts from the set of directories
# containing a go.mod on disk — in either direction:
#   - additive drift:    a go.mod directory is not declared in the workspace file
#   - subtractive drift: a declared entry points at a directory with no go.mod
# Both break `cp go.work.example go.work && go build ./...`.
set -euo pipefail

WORK_EXAMPLE="${1:-go.work.example}"

if [ ! -f "$WORK_EXAMPLE" ]; then
  echo "check-goworkexample: $WORK_EXAMPLE not found" >&2
  exit 2
fi

# Collect declared paths inside the use ( ... ) block.
declared=$(awk '/^use \(/{flag=1;next} /^\)/{flag=0} flag {gsub(/^[[:space:]]+|[[:space:]]+$/, ""); if ($0 != "") print}' "$WORK_EXAMPLE" | sort -u)

# Collect actual module directories (relative paths starting with ./).
actual=$(find . -name go.mod -not -path './.git/*' -exec dirname {} \; | sort -u)

# On disk but not declared — needs to be added to the workspace file.
missing_from_workspace=$(comm -23 <(echo "$actual") <(echo "$declared"))
# Declared but not on disk — stale entry, needs to be removed from the workspace file.
missing_from_disk=$(comm -13 <(echo "$actual") <(echo "$declared"))

status=0

if [ -n "$missing_from_workspace" ]; then
  echo "check-goworkexample: the following module directories are missing from $WORK_EXAMPLE:" >&2
  echo "$missing_from_workspace" >&2
  echo "" >&2
  echo "Add them under the use ( ... ) block so \`cp $WORK_EXAMPLE go.work && go build ./...\` works." >&2
  status=1
fi

if [ -n "$missing_from_disk" ]; then
  if [ "$status" -ne 0 ]; then
    echo "" >&2
  fi
  echo "check-goworkexample: the following entries in $WORK_EXAMPLE point at directories with no go.mod:" >&2
  echo "$missing_from_disk" >&2
  echo "" >&2
  echo "Remove these stale entries from the use ( ... ) block so \`cp $WORK_EXAMPLE go.work && go build ./...\` works." >&2
  status=1
fi

if [ "$status" -ne 0 ]; then
  exit "$status"
fi

echo "check-goworkexample: OK ($(echo "$actual" | wc -l | tr -d ' ') modules, all declared)"
