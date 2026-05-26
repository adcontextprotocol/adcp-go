#!/usr/bin/env bash
# Run the Go reference seller through the AdCP media_buy_seller storyboard.
#
# Required runner input: set exactly one of these modes.
#   ADCP_SDK_VERSION=7.11.0       Install published @adcp/sdk from npm.
#   ADCP_SDK_TARBALL=/tmp/sdk.tgz Install a candidate @adcp/sdk tarball.
#   ADCP_RUNNER_BIN=/path/to/adcp Use a preinstalled storyboard runner binary.
#
# Optional inputs:
#   ADCP_PORT=3001
#   STORYBOARD_RESULT_PATH=storyboard-result-go.json
#   SELLER_LOG_PATH=/tmp/adcp-go-reference-seller.log
#   ADCP_AGENT_URL=http://127.0.0.1:3001/mcp
#
# The script assumes it is running from an adcp-go checkout, boots the Go
# reference seller, writes the storyboard JSON result, and exits non-zero unless
# overall_status is "passing" and the test controller is detected.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

ADCP_PORT="${ADCP_PORT:-3001}"
STORYBOARD_RESULT_PATH="${STORYBOARD_RESULT_PATH:-storyboard-result-go.json}"
SELLER_LOG_PATH="${SELLER_LOG_PATH:-/tmp/adcp-go-reference-seller.log}"
ADCP_AGENT_URL="${ADCP_AGENT_URL:-http://127.0.0.1:${ADCP_PORT}/mcp}"

ADCP_RUNNER_BIN="${ADCP_RUNNER_BIN:-}"
ADCP_SDK_VERSION="${ADCP_SDK_VERSION:-}"
ADCP_SDK_TARBALL="${ADCP_SDK_TARBALL:-}"

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

runner_modes=0
[[ -n "$ADCP_RUNNER_BIN" ]] && runner_modes=$((runner_modes + 1))
[[ -n "$ADCP_SDK_TARBALL" ]] && runner_modes=$((runner_modes + 1))
[[ -n "$ADCP_SDK_VERSION" ]] && runner_modes=$((runner_modes + 1))
[[ "$runner_modes" -eq 1 ]] || fail "set exactly one of ADCP_RUNNER_BIN, ADCP_SDK_TARBALL, or ADCP_SDK_VERSION"

install_or_select_runner() {
  if [[ -n "$ADCP_RUNNER_BIN" ]]; then
    command -v "$ADCP_RUNNER_BIN" >/dev/null 2>&1 || [[ -x "$ADCP_RUNNER_BIN" ]] || \
      fail "ADCP_RUNNER_BIN is not executable or on PATH: $ADCP_RUNNER_BIN"
    echo "Using preinstalled storyboard runner: $ADCP_RUNNER_BIN"
  elif [[ -n "$ADCP_SDK_TARBALL" ]]; then
    require_command npm
    [[ -f "$ADCP_SDK_TARBALL" ]] || fail "ADCP_SDK_TARBALL not found: $ADCP_SDK_TARBALL"
    echo "Installing candidate @adcp/sdk tarball: $ADCP_SDK_TARBALL"
    npm install -g "$ADCP_SDK_TARBALL"
    ADCP_RUNNER_BIN="adcp"
  else
    require_command npm
    echo "Installing published @adcp/sdk@$ADCP_SDK_VERSION"
    npm install -g "@adcp/sdk@$ADCP_SDK_VERSION"
    ADCP_RUNNER_BIN="adcp"
  fi

  "$ADCP_RUNNER_BIN" --version
}

wait_for_seller() {
  local pid="$1"
  local url="http://127.0.0.1:${ADCP_PORT}/mcp"

  for i in $(seq 1 60); do
    local http_code
    http_code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 1 "$url" 2>/dev/null) || http_code="000"
    if [[ "$http_code" != "000" ]]; then
      echo "Seller agent ready (HTTP ${http_code}, pid ${pid})"
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "Seller agent process died during startup"
      tail -n 80 "$SELLER_LOG_PATH" 2>/dev/null || true
      return 1
    fi
    if [[ "$i" -eq 60 ]]; then
      echo "Seller agent failed to start within 30s"
      tail -n 80 "$SELLER_LOG_PATH" 2>/dev/null || true
      return 1
    fi
    sleep 0.5
  done
}

assert_storyboard_passed() {
  require_command python3
  python3 - "$STORYBOARD_RESULT_PATH" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
if not path.exists() or path.stat().st_size == 0:
    print(f"{path} missing or empty; runner produced no output")
    sys.exit(1)

with path.open() as f:
    doc = json.load(f)

if doc.get("overall_status") != "passing":
    print(json.dumps(doc, indent=2))
    sys.exit(1)

if not doc.get("controller_detected"):
    print("controller_detected was false; check reference seller test controller wiring")
    sys.exit(1)

summary = doc.get("summary") or {}
print("Storyboard passing.")
if summary:
    print("summary:", json.dumps(summary, sort_keys=True))
PY
}

cleanup() {
  if [[ -n "${SELLER_PID:-}" ]]; then
    kill "$SELLER_PID" 2>/dev/null || true
    wait "$SELLER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

require_command go
require_command curl
install_or_select_runner

mkdir -p "$(dirname "$STORYBOARD_RESULT_PATH")"
echo "Starting Go reference seller on port $ADCP_PORT"
(
  cd reference/seller-agent
  PORT="$ADCP_PORT" ADCP_AGENT_URL="$ADCP_AGENT_URL" \
    go run ./cmd/seller-agent
) >"$SELLER_LOG_PATH" 2>&1 &
SELLER_PID=$!
wait_for_seller "$SELLER_PID"

echo "Running media_buy_seller storyboard"
set +e
"$ADCP_RUNNER_BIN" storyboard run \
  "http://127.0.0.1:${ADCP_PORT}/mcp" media_buy_seller \
  --json --allow-http \
  >"$STORYBOARD_RESULT_PATH"
RUNNER_STATUS=$?
set -e

if [[ "$RUNNER_STATUS" -ne 0 ]]; then
  echo "Storyboard runner exited with status $RUNNER_STATUS"
  [[ -s "$STORYBOARD_RESULT_PATH" ]] && cat "$STORYBOARD_RESULT_PATH"
  exit "$RUNNER_STATUS"
fi

assert_storyboard_passed
