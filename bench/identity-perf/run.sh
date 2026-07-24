#!/usr/bin/env bash
# run.sh — sweep (cpu, memory) x scenario x qps, save per-run JSON reports
# and a rolled-up CSV summary.
#
# Usage:
#   ./run.sh                          # full matrix
#   ./run.sh fcap-only                # single scenario
#   ./run.sh fcap-audience 2 4g       # single scenario, single config
#
# Requires: docker >= 24 with compose v2. Run from bench/identity-perf/.
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

echo "scenario,cpus,memory_gb,target_qps,concurrency,achieved_qps,p50_latency_ms,p90_latency_ms,p99_latency_ms,p999_latency_ms,ok_2xx,non_2xx,errors,qps_per_core,identity_rss_peak_mb,audience_valkey_rss_peak_mb,fcap_valkey_rss_peak_mb,identity_cpu_peak_pct,audience_valkey_cpu_peak_pct,fcap_valkey_cpu_peak_pct" > "$SUMMARY_CSV"

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

# One `docker stats --no-stream` call covering all three containers we care
# about (identity-agent + both valkeys), tagged by container name so the
# post-run peak extractor can split by service. Robust to both docker CE
# MemUsage layouts ('123.5MiB' and '123.5 MiB'); POSIX awk only so this
# works on macOS BSD nawk and Linux gawk.
sample_stats() {
  local identity=$1 audience=$2 fcap=$3
  if [[ -z "$identity" && -z "$audience" && -z "$fcap" ]]; then return; fi
  # {{.Name}} <mem-usage> <mem-unit-maybe> ... <cpu%>
  docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.CPUPerc}}' \
    "$identity" "$audience" "$fcap" 2>/dev/null \
    | awk '{
        name = $1
        # MemUsage is "used / limit"; used may be "123MiB" or "123 MiB".
        if ($2 ~ /(KiB|MiB|GiB)$/) { used = $2; slash_at = 3 }
        else                       { used = $2 $3; slash_at = 4 }
        cpu = $NF
        sub(/%/, "", cpu)
        rss_mb = 0
        if (used ~ /GiB$/) { sub(/GiB$/, "", used); rss_mb = used * 1024 }
        else if (used ~ /MiB$/) { sub(/MiB$/, "", used); rss_mb = used }
        else if (used ~ /KiB$/) { sub(/KiB$/, "", used); rss_mb = used / 1024 }
        printf "%s %.2f %s\n", name, rss_mb, cpu
      }'
}

# Extract the peak MB or CPU% seen for a given container-name substring in the
# streaming stats log. Empty log or no match returns 0.
peak_from_stats() {
  local stats_log=$1 name_needle=$2 col=$3  # col: 2=rss_mb, 3=cpu%
  awk -v n="$name_needle" -v c="$col" '
    BEGIN { m = 0 }
    index($1, n) > 0 { v = $c + 0; if (v > m) m = v }
    END { printf "%.2f\n", m }
  ' "$stats_log" 2>/dev/null || echo 0
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

    # `--profile baseline` activates the standalone audience-valkey /
    # fcap-valkey services (profile-gated so cluster topologies via
    # run-scaling.sh don't spin them up idle). Named-service startup
    # would also work but this is explicit.
    IDENTITY_CPUS="$cpus" \
    IDENTITY_MEMORY="$memory" \
      docker compose --profile baseline up -d audience-valkey fcap-valkey configserver
    IDENTITY_CPUS="$cpus" \
    IDENTITY_MEMORY="$memory" \
      docker compose --profile baseline up -d --force-recreate identity-agent

    # docker compose's `deploy.resources.limits` isn't reliably honored
    # outside swarm mode on Linux/cgroup-v2, so force the cgroup via
    # `docker update` and verify the resulting NanoCpus/Memory match what
    # we asked for. This is the only way to be sure the reported qps_per_core
    # numbers actually reflect a CPU-capped container.
    identity_cid=$(docker compose ps -q identity-agent)
    docker update --cpus "$cpus" --memory "$memory" --memory-swap "$memory" \
      "$identity_cid" >/dev/null
    want_nanocpus=$(awk -v c="$cpus" 'BEGIN{printf "%d", c*1000000000}')
    got_nanocpus=$(docker inspect --format '{{.HostConfig.NanoCpus}}' "$identity_cid")
    got_mem_bytes=$(docker inspect --format '{{.HostConfig.Memory}}' "$identity_cid")
    if [[ "$got_nanocpus" != "$want_nanocpus" ]]; then
      echo "!! CPU limit not applied: want NanoCpus=$want_nanocpus got $got_nanocpus" >&2
      exit 1
    fi
    if (( got_mem_bytes == 0 )); then
      echo "!! Memory limit not applied on identity-agent" >&2
      exit 1
    fi
    echo "  identity-agent limits enforced: NanoCpus=$got_nanocpus MemBytes=$got_mem_bytes"

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

      # Stats sampler runs in the background: identity-agent + both valkeys.
      # Container IDs resolved once up front so the per-tick call doesn't
      # re-invoke docker compose ps.
      stats_log="$scenario_dir/stats_${qps}qps.log"
      : > "$stats_log"
      identity_cid=$(docker compose ps -q identity-agent)
      audience_cid=$(docker compose ps -q audience-valkey)
      fcap_cid=$(docker compose ps -q fcap-valkey)
      (
        while true; do
          sample_stats "$identity_cid" "$audience_cid" "$fcap_cid" >> "$stats_log" 2>/dev/null || true
          sleep 1
        done
      ) &
      sampler_pid=$!
      # Kill the sampler if the script exits before the explicit kill below
      # (Ctrl-C, set -e failure) so we don't leave orphan pollers behind.
      trap 'kill '"$sampler_pid"' 2>/dev/null || true' EXIT INT TERM

      # IDENTITY_CPUS / IDENTITY_MEMORY have to be passed on every compose
      # invocation, not just the identity-agent `up`. Otherwise `docker
      # compose run --rm loadgen` re-evaluates the compose file with those
      # vars unset, sees the identity-agent service's resolved config
      # (cpus/mem_limit) differs from the running container, and silently
      # recreates identity-agent with the default 2 CPU / 2 GB caps — which
      # is exactly the "why is my 1c container using 202% CPU" bug.
      IDENTITY_CPUS="$cpus" IDENTITY_MEMORY="$memory" \
      QPS="$qps" DURATION="$DURATION" WARMUP="$WARMUP" CONCURRENCY="$CONCURRENCY" \
        LABEL="$label" REPORT="$report_path" \
        docker compose run --rm loadgen 2>&1 | tee "$scenario_dir/loadgen_${qps}qps.log" || true

      # Recheck the container ID didn't change out from under us; if it did,
      # `docker compose run` recreated identity-agent and the sampler was
      # measuring the wrong container. Fail loud so this can't produce
      # invalid data silently.
      post_cid=$(docker compose ps -q identity-agent)
      if [[ "$post_cid" != "$identity_cid" ]]; then
        echo "!! identity-agent container changed during loadgen (was $identity_cid, now $post_cid)" >&2
        exit 1
      fi

      kill "$sampler_pid" 2>/dev/null || true
      wait "$sampler_pid" 2>/dev/null || true
      trap - EXIT INT TERM

      # Also save identity-agent /metrics snapshot after each step.
      curl -sf "http://127.0.0.1:${HOST_METRICS_PORT:-9464}/metrics" \
        > "$scenario_dir/metrics_${qps}qps.prom" 2>/dev/null || true

      if [[ -f "$host_report" ]]; then
        id_rss=$(peak_from_stats "$stats_log" "identity-agent"   2)
        aud_rss=$(peak_from_stats "$stats_log" "audience-valkey" 2)
        fc_rss=$(peak_from_stats "$stats_log" "fcap-valkey"      2)
        id_cpu=$(peak_from_stats "$stats_log" "identity-agent"   3)
        aud_cpu=$(peak_from_stats "$stats_log" "audience-valkey" 3)
        fc_cpu=$(peak_from_stats "$stats_log" "fcap-valkey"      3)
        python3 - "$host_report" "$scenario" "$cpus" "$memory" "$CONCURRENCY" \
          "$id_rss" "$aud_rss" "$fc_rss" "$id_cpu" "$aud_cpu" "$fc_cpu" \
          >> "$SUMMARY_CSV" <<'PY'
import json, sys
p, scenario, cpus, memory, conc, id_rss, aud_rss, fc_rss, id_cpu, aud_cpu, fc_cpu = sys.argv[1:]
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
    f"{qps_per_core:.1f}",
    id_rss, aud_rss, fc_rss, id_cpu, aud_cpu, fc_cpu,
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
# `column` isn't installed by default on Ubuntu 24.04; fall through to plain
# cat so the summary is always printed.
if command -v column >/dev/null 2>&1; then
  column -t -s , "$SUMMARY_CSV"
else
  cat "$SUMMARY_CSV"
fi
