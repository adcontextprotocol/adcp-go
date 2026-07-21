#!/usr/bin/env bash
# run.sh — sweep (cpu, memory) x scenario x qps, save per-run JSON reports
# and a rolled-up CSV summary.
#
# Usage:
#   ./run.sh                          # full matrix
#   ./run.sh fcap-only                # single scenario
#   ./run.sh fcap-audience 2 4g       # single scenario, single config
#
# Requires: docker >= 24 with compose v2. Run from bench/perf/.
set -euo pipefail

cd "$(dirname "$0")"

RESULTS_DIR="${RESULTS_DIR:-$(pwd)/results/$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$RESULTS_DIR"
SUMMARY_CSV="$RESULTS_DIR/summary.csv"
# Exported so docker-compose substitutes it into the loadgen volume mount.
export RESULTS_DIR

# --- matrix ------------------------------------------------------------------
CONFIGS=(
  "1|1g"
  "1|2g"
  "2|2g"
  "2|4g"
  "4|4g"
  "4|8g"
  "8|8g"
)
read -r -a QPS_STEPS <<< "${QPS_STEPS:-500 1000 2000 4000 8000 16000 32000}"
DURATION="${DURATION:-30s}"
WARMUP="${WARMUP:-5s}"
CONCURRENCY="${CONCURRENCY:-256}"

SCENARIOS=(fcap-only fcap-audience)
case $# in
  0) ;;
  1) SCENARIOS=("$1") ;;
  3) SCENARIOS=("$1"); CONFIGS=("$2|$3") ;;
  *)
    echo "usage: $0 [scenario] [cpus memory]" >&2
    echo "  no args   -> sweep both scenarios and full CONFIGS matrix"
    echo "  1 arg     -> single scenario, full CONFIGS matrix"
    echo "  3 args    -> single scenario, single (cpus, memory) config"
    exit 2
    ;;
esac

echo "scenario,cpus,memory_gb,target_qps,concurrency,achieved_qps,p50_latency_ms,p90_latency_ms,p99_latency_ms,p999_latency_ms,ok_2xx,non_2xx,errors,qps_per_core,rss_peak_mb" > "$SUMMARY_CSV"

# --- helpers -----------------------------------------------------------------
wait_healthy() {
  local timeout=${1:-60}
  local port="${HOST_METRICS_PORT:-9464}"
  local end=$(( $(date +%s) + timeout ))
  # identity-agent's runtime image is distroless/static — no shell or wget
  # inside the container. Poll /health from the host on the exposed port.
  while (( $(date +%s) < end )); do
    if curl -sf "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# Sample the identity-agent container's RSS in MB. Robust to both docker CE
# formats: '123.5MiB' and '123.5 MiB' (some versions add a space). Uses only
# POSIX awk features so it works with BSD nawk on macOS and gawk on Linux.
sample_rss_mb() {
  local cid=$1
  if [[ -z "$cid" ]]; then echo 0; return; fi
  docker stats --no-stream --format '{{.MemUsage}}' "$cid" 2>/dev/null \
    | awk '{
        used = $1
        if (used ~ /GiB$/) { sub(/GiB$/, "", used); printf "%.1f\n", used * 1024 }
        else if (used ~ /MiB$/) { sub(/MiB$/, "", used); printf "%.1f\n", used }
        else if (used ~ /KiB$/) { sub(/KiB$/, "", used); printf "%.3f\n", used / 1024 }
        else if ($2 ~ /GiB/) { printf "%.1f\n", $1 * 1024 }
        else if ($2 ~ /MiB/) { printf "%.1f\n", $1 }
        else if ($2 ~ /KiB/) { printf "%.3f\n", $1 / 1024 }
        else print 0
      }'
}

# --- build once --------------------------------------------------------------
# Build services sequentially. configserver/seeder/loadgen all share the same
# adcp-perf-tools image tag, so a parallel `docker compose build` racing on the
# tag write fails with "image already exists" on some BuildKit versions.
echo "==> building images"
for svc in identity-agent configserver seeder loadgen; do
  docker compose build "$svc"
done

for scenario in "${SCENARIOS[@]}"; do
  scenario_env="scenarios/${scenario}.env"
  if [[ ! -f "$scenario_env" ]]; then
    echo "!! scenario env not found: $scenario_env" >&2
    continue
  fi
  # shellcheck disable=SC2046
  set -a
  # shellcheck source=/dev/null
  . "$scenario_env"
  set +a

  for cfg in "${CONFIGS[@]}"; do
    cpus="${cfg%|*}"
    memory="${cfg#*|}"

    run_id="${scenario}_${cpus}c_${memory}"
    scenario_dir="$RESULTS_DIR/$run_id"
    mkdir -p "$scenario_dir"
    echo
    echo "==================================================================="
    echo "SCENARIO=$scenario CPUS=$cpus MEMORY=$memory"
    echo "==================================================================="

    IDENTITY_CPUS="$cpus" \
    IDENTITY_MEMORY="$memory" \
      docker compose up -d audience-valkey fcap-valkey configserver
    IDENTITY_CPUS="$cpus" \
    IDENTITY_MEMORY="$memory" \
      docker compose up -d --force-recreate identity-agent

    echo "  waiting for identity-agent /health"
    if ! wait_healthy 90; then
      echo "!! identity-agent did not become healthy" >&2
      docker compose logs --tail=200 identity-agent
      continue
    fi

    echo "  seeding Valkey"
    docker compose run --rm seeder >"$scenario_dir/seeder.log" 2>&1 || {
      echo "!! seeder failed"; cat "$scenario_dir/seeder.log" >&2; continue;
    }

    for qps in "${QPS_STEPS[@]}"; do
      label="${run_id}_${qps}qps"
      report_path="/results/${scenario}_${cpus}c_${memory}_${qps}qps.json"
      host_report="$RESULTS_DIR/${scenario}_${cpus}c_${memory}_${qps}qps.json"
      echo "  -> qps=$qps duration=$DURATION concurrency=$CONCURRENCY"

      # RSS peak sampler runs in the background. Container ID resolved once
      # up front so the per-tick sample doesn't re-invoke docker compose ps.
      rss_log="$scenario_dir/rss_${qps}qps.log"
      : > "$rss_log"
      cid=$(docker compose ps -q identity-agent)
      (
        while true; do
          sample_rss_mb "$cid" >> "$rss_log" 2>/dev/null || true
          sleep 1
        done
      ) &
      sampler_pid=$!
      # Kill the sampler if the script exits before the explicit kill below
      # (Ctrl-C, set -e failure) so we don't leave orphan pollers behind.
      trap 'kill '"$sampler_pid"' 2>/dev/null || true' EXIT INT TERM

      QPS="$qps" DURATION="$DURATION" WARMUP="$WARMUP" CONCURRENCY="$CONCURRENCY" \
        LABEL="$label" REPORT="$report_path" \
        docker compose run --rm loadgen 2>&1 | tee "$scenario_dir/loadgen_${qps}qps.log" || true

      kill "$sampler_pid" 2>/dev/null || true
      wait "$sampler_pid" 2>/dev/null || true
      trap - EXIT INT TERM

      # Also save identity-agent /metrics snapshot after each step.
      curl -sf "http://127.0.0.1:${HOST_METRICS_PORT:-9464}/metrics" \
        > "$scenario_dir/metrics_${qps}qps.prom" 2>/dev/null || true

      if [[ -f "$host_report" ]]; then
        # roll up into summary.csv
        rss_peak_mb=$(awk 'BEGIN{m=0} {if ($1+0 > m) m=$1+0} END{printf "%.1f", m}' "$rss_log")
        python3 - "$host_report" "$scenario" "$cpus" "$memory" "$CONCURRENCY" "$rss_peak_mb" >> "$SUMMARY_CSV" <<'PY'
import json, sys
p, scenario, cpus, memory, conc, rss = sys.argv[1:]
r = json.load(open(p))
qps_per_core = r.get("achieved_qps",0) / max(float(cpus),1e-9)
row = [
    scenario, cpus, memory, r.get("target_qps",""), conc,
    f"{r.get('achieved_qps',0):.1f}",
    f"{r.get('p50_ms',0):.2f}",
    f"{r.get('p90_ms',0):.2f}",
    f"{r.get('p99_ms',0):.2f}",
    f"{r.get('p99_9_ms',0):.2f}",
    r.get("ok_2xx",0), r.get("non_2xx",0), r.get("transport_errors",0),
    f"{qps_per_core:.1f}", rss,
]
print(",".join(str(c) for c in row))
PY
      fi
    done

    echo "  tearing down identity-agent (keeping valkey)"
    docker compose stop identity-agent >/dev/null
  done
done

docker compose down -v --remove-orphans >/dev/null 2>&1 || true

echo
echo "=== done ==="
echo "results in: $RESULTS_DIR"
echo "summary:    $SUMMARY_CSV"
column -t -s , "$SUMMARY_CSV"
