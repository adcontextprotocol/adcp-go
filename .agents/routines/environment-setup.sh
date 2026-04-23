#!/bin/bash
# Cloud environment setup for adcp-go routines.
# Paste into the "Setup script" field when creating the routine's
# environment at claude.ai/code/routines. Runs as root on Ubuntu 24.04;
# result is cached ~7 days.

set -euo pipefail

# gh CLI for `gh issue`, `gh pr create`, etc. — not pre-installed.
apt-get update
apt-get install -y gh

# Go toolchain is pre-installed (latest stable with module support).
# Warm the module cache so subsequent builds don't hit the network.
if [ -f go.mod ]; then
  go mod download || true
fi

# golangci-lint (not pre-installed). Install the pinned version.
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
  | sh -s -- -b "$(go env GOPATH)/bin" v1.62.2

echo "Setup complete."
