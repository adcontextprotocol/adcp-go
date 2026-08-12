#!/usr/bin/env bash
# run.sh — bring up the containerized end-to-end stack, drive it, tear it down.
#
# Usage:
#   ./run.sh                     # build the three images from this working copy
#   ./run.sh --published         # pull the published images at :edge
#   ./run.sh --published v1.2.3  # pull the published images at a given tag
#
# Environment:
#   KEEP_STACK=1     leave the stack running after the verdict (for poking at it)
#   E2E_SUBNET       override the container subnet (default 100.64.0.0/24)
#   ROUTER_PORT (19080), ROUTER_ADMIN_PORT (19090),
#   CONTEXT_AGENT_PORT (19081), CONTEXT_AGENT_ADMIN_PORT (19091),
#   IDENTITY_AGENT_PORT (19082), IDENTITY_AGENT_ADMIN_PORT (19092),
#   REGISTRY_PORT (19101), CONFIG_PORT (19102), VALKEY_PORT (16379)
#                    host ports, all bound to 127.0.0.1
#
# Requires docker >= 24 with compose v2, and curl.
set -euo pipefail

# Resolve the script's own path before changing directory, so --help can still
# read the usage block out of this file.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
readonly SCRIPT="${SCRIPT_DIR}/$(basename "$0")"
cd "$SCRIPT_DIR"

readonly GHCR_PREFIX="ghcr.io/adcontextprotocol/adcp-go"

ROUTER_PORT="${ROUTER_PORT:-19080}"
ROUTER_ADMIN_PORT="${ROUTER_ADMIN_PORT:-19090}"
CONTEXT_AGENT_PORT="${CONTEXT_AGENT_PORT:-19081}"
CONTEXT_AGENT_ADMIN_PORT="${CONTEXT_AGENT_ADMIN_PORT:-19091}"
IDENTITY_AGENT_PORT="${IDENTITY_AGENT_PORT:-19082}"
IDENTITY_AGENT_ADMIN_PORT="${IDENTITY_AGENT_ADMIN_PORT:-19092}"
REGISTRY_PORT="${REGISTRY_PORT:-19101}"
CONFIG_PORT="${CONFIG_PORT:-19102}"
VALKEY_PORT="${VALKEY_PORT:-16379}"

# Bind every published port to loopback. The stack has no authentication worth
# the name — the stub tokens are in the repository — so it must not be
# reachable from off-box.
export E2E_ROUTER_PORT="127.0.0.1:${ROUTER_PORT}"
export E2E_ROUTER_ADMIN_PORT="127.0.0.1:${ROUTER_ADMIN_PORT}"
export E2E_CONTEXT_AGENT_PORT="127.0.0.1:${CONTEXT_AGENT_PORT}"
export E2E_CONTEXT_AGENT_ADMIN_PORT="127.0.0.1:${CONTEXT_AGENT_ADMIN_PORT}"
export E2E_IDENTITY_AGENT_PORT="127.0.0.1:${IDENTITY_AGENT_PORT}"
export E2E_IDENTITY_AGENT_ADMIN_PORT="127.0.0.1:${IDENTITY_AGENT_ADMIN_PORT}"
export E2E_REGISTRY_PORT="127.0.0.1:${REGISTRY_PORT}"
export E2E_CONFIG_PORT="127.0.0.1:${CONFIG_PORT}"
export E2E_VALKEY_PORT="127.0.0.1:${VALKEY_PORT}"

IMAGE_SOURCE="local"
IMAGE_TAG="edge"
case "${1:-}" in
  "")           ;;
  --published)  IMAGE_SOURCE="published"; IMAGE_TAG="${2:-edge}" ;;
  # Print the header comment block above, minus the leading '# '.
  -h|--help)    awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$SCRIPT"; exit 0 ;;
  *)
    echo "!! unknown argument: $1" >&2
    echo "usage: $0 [--published [tag]]" >&2
    exit 2
    ;;
esac

if [[ "$IMAGE_SOURCE" == "published" ]]; then
  export E2E_ROUTER_IMAGE="${GHCR_PREFIX}/router:${IMAGE_TAG}"
  export E2E_CONTEXT_AGENT_IMAGE="${GHCR_PREFIX}/context-agent:${IMAGE_TAG}"
  export E2E_IDENTITY_AGENT_IMAGE="${GHCR_PREFIX}/identity-agent:${IMAGE_TAG}"
else
  export E2E_ROUTER_IMAGE="adcp-router:e2e"
  export E2E_CONTEXT_AGENT_IMAGE="adcp-context-agent:e2e"
  export E2E_IDENTITY_AGENT_IMAGE="adcp-identity-agent:e2e"
fi

# --- helpers -----------------------------------------------------------------

log() { printf '==> %s\n' "$*"; }

fail() {
  printf '!! %s\n' "$*" >&2
  exit 1
}

# wait_for polls a URL until it answers 200, optionally requiring a substring
# in the body. Both agents and the router are distroless, so they carry no
# compose health check — this is how the script observes their readiness.
wait_for() {
  local name=$1 url=$2 needle=${3:-} timeout=${4:-120} body
  local end=$(( $(date +%s) + timeout ))
  while (( $(date +%s) < end )); do
    if body=$(curl -sf --max-time 5 "$url" 2>/dev/null); then
      if [[ -z "$needle" || "$body" == *"$needle"* ]]; then
        log "ready: $name"
        return 0
      fi
    fi
    sleep 1
  done
  fail "$name did not become ready within ${timeout}s ($url)"
}

dump_logs() {
  printf '\n--- container logs ---\n' >&2
  docker compose logs --tail=120 --no-color >&2 || true
}

teardown() {
  if [[ "${KEEP_STACK:-0}" == "1" ]]; then
    log "KEEP_STACK=1 — leaving the stack up"
    log "  router      http://127.0.0.1:${ROUTER_PORT}"
    log "  router admin http://127.0.0.1:${ROUTER_ADMIN_PORT}/providers"
    log "  tear down with: docker compose -f $(pwd)/docker-compose.yml down -v"
    return
  fi
  log "tearing down"
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
}

# Every non-zero exit from here on — an unguarded `docker compose` under
# `set -e` included — dumps container logs and tears the stack down. Without
# this a crash-looping stub kills the script silently and CI reports a failure
# with nothing to read.
on_exit() {
  local rc=$?
  if (( rc != 0 )); then
    dump_logs
  fi
  teardown
  exit "$rc"
}
trap on_exit EXIT

# --- images ------------------------------------------------------------------

log "building the tools image"
docker compose build registrystub

if [[ "$IMAGE_SOURCE" == "published" ]]; then
  log "pulling published images at :${IMAGE_TAG}"
  docker compose pull router context-agent identity-agent
else
  log "building router, context-agent and identity-agent from this working copy"
  docker build -q -t "$E2E_ROUTER_IMAGE" -f ../../cmd/router/Dockerfile ../.. >/dev/null
  docker build -q -t "$E2E_CONTEXT_AGENT_IMAGE" -f ../../cmd/context-agent/Dockerfile ../.. >/dev/null
  docker build -q -t "$E2E_IDENTITY_AGENT_IMAGE" -f ../../cmd/identity-agent/Dockerfile ../.. >/dev/null
fi

# --- bring the stack up in dependency order ----------------------------------

log "clearing any previous run"
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

log "starting valkey"
docker compose up -d --wait --quiet-pull valkey

# Seeding before the context-agent starts means the suppression snapshot the
# agent loads at boot is already populated, so the kill-switch scenario does
# not depend on a refresh landing mid-run.
log "seeding valkey"
docker compose run --rm seed || fail "seeder failed"

# The registry stub blocks on the router's public JWK, and the router needs the
# generated config, so bootstrap runs before either.
log "generating the router signing key and config"
docker compose run --rm bootstrap || fail "bootstrap failed"

log "starting the registry and identity-config stubs"
docker compose up -d --wait registrystub configstub

log "starting the router"
docker compose up -d router
wait_for "router /healthz" "http://127.0.0.1:${ROUTER_PORT}/healthz"
# The identity-agent's snapshot keystore does one synchronous fetch at boot and
# then refuses traffic without a key, so the router has to have merged its
# signing key into the snapshot first. `signing_keys` is omitempty on every
# property record, so the field appearing at all is exactly that condition —
# and needing no key id here keeps run.sh from duplicating one from the fixture.
wait_for "router /registry/snapshot carries a signing key" \
  "http://127.0.0.1:${ROUTER_PORT}/registry/snapshot" '"signing_keys"'

log "starting the agents"
docker compose up -d context-agent identity-agent
wait_for "context-agent /health" "http://127.0.0.1:${CONTEXT_AGENT_PORT}/health"
wait_for "identity-agent /health" "http://127.0.0.1:${IDENTITY_AGENT_PORT}/health"

# --- verdict -----------------------------------------------------------------

log "running the verifier"
set +e
docker compose run --rm verify
verdict=$?
set -e

if (( verdict != 0 )); then
  printf '\n!! verification failed (exit %d)\n' "$verdict" >&2
  exit "$verdict"
fi

log "end-to-end stack passed"
