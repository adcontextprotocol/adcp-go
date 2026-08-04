#!/usr/bin/env bash
# Fails if any directory containing a go.mod is missing from go.work.example's use () block.
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

missing=$(comm -23 <(echo "$actual") <(echo "$declared"))

if [ -n "$missing" ]; then
  echo "check-goworkexample: the following module directories are missing from $WORK_EXAMPLE:" >&2
  echo "$missing" >&2
  echo "" >&2
  echo "Add them under the use ( ... ) block so \`cp $WORK_EXAMPLE go.work && go build ./...\` works." >&2
  exit 1
fi

echo "check-goworkexample: OK ($(echo "$actual" | wc -l | tr -d ' ') modules, all declared)"
