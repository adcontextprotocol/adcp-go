#!/bin/bash
# Cloud environment setup for adcp-go routines.
# Paste into the "Setup script" field at claude.ai/code/routines.
# Runs as root on Ubuntu 24.04; result is cached ~7 days.

set -euo pipefail

# gh CLI from GitHub's official apt repo.
apt-get update
apt-get install -y ca-certificates curl gnupg
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
  | gpg --dearmor -o /etc/apt/keyrings/githubcli-archive-keyring.gpg
chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
  > /etc/apt/sources.list.d/github-cli.list
apt-get update
apt-get install -y gh

# Warm the module cache. Fail loudly if modules can't resolve — the
# previous `|| true` hid real errors.
if [ -f go.mod ]; then
  go mod download
fi

# golangci-lint. Pin the installer URL to the version tag (not master)
# so the bootstrap script is also reproducible.
GOLANGCI_VERSION=v1.62.2
curl -sSfL "https://raw.githubusercontent.com/golangci/golangci-lint/${GOLANGCI_VERSION}/install.sh" \
  | sh -s -- -b "$(go env GOPATH)/bin" "${GOLANGCI_VERSION}"

echo "Setup complete."
