#!/usr/bin/env bash
#
# Print the SOURCE_DATE_EPOCH the tmp-router reproducible build uses:
# the commit time of the last commit that touched a build input.
#
# Called by both scripts/build-tmp-router.sh (local rebuilds) and
# .github/workflows/tmp-router-image.yml (CI) so the path list can never
# drift between them — a drift would cause CI and a local auditor to
# compute different SDE values, produce different image digests, and
# make the documented reproducibility check fail against a genuinely-
# reproducible build.
#
# Emits 0 when no matching commit is found (e.g., a shallow clone whose
# tip does not touch a build path). CI's Verify-reproducibility step
# treats both sides identically because both go through this script.

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SDE="$(git log -1 --format=%ct -- \
  cmd/router/Dockerfile \
  cmd/router/ \
  router/ \
  tmproto/ \
  targeting/ \
  urlcanon/ \
  go.mod \
  go.sum \
  2>/dev/null || echo 0)"

# `git log -1 -- <paths>` exits 0 with empty stdout when the path filter
# matches no commit — the `|| echo 0` only fires on git itself failing.
# The empty-string guard below is the one that catches "no commit found."
if [[ -z "$SDE" ]]; then
  SDE=0
fi

echo "$SDE"
