#!/usr/bin/env bash
# run-scaling.sh — Valkey topology scaling sweep. For each Valkey topology
# (standalone, shadow-N, cluster-N), pin identity-agent at a fixed size and
# ramp QPS until either Valkey or identity-agent saturates. Produces a
# summary.csv with per-topology throughput and per-shard CPU peaks so the
# sizing recommendation ("when to shard, how to shard") can cite specific
# measurements.
#
# Usage:
#   ./run-scaling.sh                # full topology sweep
#   ./run-scaling.sh standalone     # single topology
#
# Requires: docker >= 24, compose v2. Run from bench/perf/.
set -euo pipefail

cd "$(dirname "$0")"

RESULTS_DIR="${RESULTS_DIR:-$(pwd)/results/scaling-$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$RESULTS_DIR"
SUMMARY_CSV="$RESULTS_DIR/summary.csv"
export RESULTS_DIR

IDENTITY_CPUS="${IDENTITY_CPUS:-8}"
IDENTITY_MEMORY="${IDENTITY_MEMORY:-8g}"
DURATION="${DURATION:-30s}"
WARMUP="${WARMUP:-5s}"
CONCURRENCY="${CONCURRENCY:-512}"
read -r -a QPS_STEPS <<< "${QPS_STEPS:-2000 4000 8000 12000 16000 24000}"

# Write-load knobs. Set WRITE_QPS_FCAP to a nonzero value to run a
# realistic mixed workload — the writer streams HSETEX cap markers at
# that rate against the same Valkey backends the loadgen reads from.
# WRITE_QPS_AUDIENCE is off by default (0) because audience updates
# tend to be batched off-peak in production.
WRITE_QPS_FCAP="${WRITE_QPS_FCAP:-0}"
WRITE_QPS_AUDIENCE="${WRITE_QPS_AUDIENCE:-0}"
PACKAGES_PER_WRITE="${PACKAGES_PER_WRITE:-2}"
AUDIENCES_PER_WRITE="${AUDIENCES_PER_WRITE:-1}"
export WRITE_QPS_FCAP WRITE_QPS_AUDIENCE PACKAGES_PER_WRITE AUDIENCES_PER_WRITE

# Topology descriptors (space-separated): name|mode|nshards|profiles
# `profiles` is a comma-separated list of compose profiles to activate.
ALL_TOPOLOGIES=(
  "standalone-1|standalone|1|"
  "shadow-2|shadow|2|shadow"
  "shadow-4|shadow|4|shadow"
  "cluster-3|cluster|3|cluster"
  "cluster-6|cluster|6|cluster,cluster-large"
)
TOPOLOGIES=("${ALL_TOPOLOGIES[@]}")
if [[ $# -ge 1 ]]; then
  filter=$1
  TOPOLOGIES=()
  for t in "${ALL_TOPOLOGIES[@]}"; do
    [[ "${t%%|*}" == "$filter" ]] && TOPOLOGIES+=("$t")
  done
  [[ ${#TOPOLOGIES[@]} -eq 0 ]] && { echo "unknown topology: $filter" >&2; exit 2; }
fi

# ---- shared scenario knobs -----------------------------------------------
set -a
# shellcheck source=scenarios/valkey-scaling.env
. scenarios/valkey-scaling.env
set +a

# ---- helpers -------------------------------------------------------------
wait_healthy() {
  local timeout=${1:-90} port="${HOST_METRICS_PORT:-9464}"
  local end=$(( $(date +%s) + timeout ))
  while (( $(date +%s) < end )); do
    curl -sf "http://127.0.0.1:${port}/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

sample_stats() {
  local names=("$@")
  [[ ${#names[@]} -eq 0 ]] && return
  docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.CPUPerc}}' "${names[@]}" 2>/dev/null \
    | awk '{
        name = $1
        if ($2 ~ /(KiB|MiB|GiB)$/) { used = $2 }
        else                       { used = $2 $3 }
        cpu = $NF; sub(/%/, "", cpu)
        rss = 0
        if (used ~ /GiB$/) { sub(/GiB$/, "", used); rss = used * 1024 }
        else if (used ~ /MiB$/) { sub(/MiB$/, "", used); rss = used }
        else if (used ~ /KiB$/) { sub(/KiB$/, "", used); rss = used / 1024 }
        printf "%s %.2f %s\n", name, rss, cpu
      }'
}

peak_from_stats() {
  local stats_log=$1 name_prefix=$2 col=$3
  awk -v n="$name_prefix" -v c="$col" '
    index($1, n) == 1 { v = $c + 0; if (v > m) m = v }
    END { printf "%.2f\n", m + 0 }
  ' "$stats_log" 2>/dev/null || echo 0
}

# _dur_seconds parses a subset of Go-duration strings (`30s`, `1m`, `2m30s`)
# into whole seconds so run-scaling.sh can compute the writer's lifetime
# budget from the QPS-step DURATION+WARMUP.
_dur_seconds() {
  local s=$1 total=0 num
  while [[ -n "$s" ]]; do
    num=$(printf '%s' "$s" | awk '{
      i=0
      while (i < length($0) && substr($0,i+1,1) ~ /[0-9.]/) i++
      print substr($0,1,i)
    }')
    [[ -z "$num" ]] && break
    s=${s#"$num"}
    case "$s" in
      s*) total=$(awk -v t="$total" -v n="$num" 'BEGIN{printf "%d", t + n}'); s=${s#s} ;;
      m*) total=$(awk -v t="$total" -v n="$num" 'BEGIN{printf "%d", t + n*60}'); s=${s#m} ;;
      h*) total=$(awk -v t="$total" -v n="$num" 'BEGIN{printf "%d", t + n*3600}'); s=${s#h} ;;
      *)  break ;;
    esac
  done
  echo "$total"
}

# fcap_shard_json / audience_shard_json print the JSON expected by
# FCAP_VALKEY_SHARDS / AUDIENCE_VALKEY_SHARDS for a given topology.
fcap_shard_json() {
  local mode=$1 n=$2
  case "$mode" in
    standalone) echo '{"0":"fcap-valkey:6379"}' ;;
    shadow)
      local names=(fcap-valkey)
      for ((i=2; i<=n; i++)); do names+=("fcap-valkey-$i"); done
      json_shards "${names[@]}"
      ;;
    cluster)
      local names=()
      for ((i=1; i<=n; i++)); do names+=("fcap-cluster-$i"); done
      json_shards "${names[@]}"
      ;;
  esac
}
audience_shard_json() {
  local mode=$1 n=$2
  case "$mode" in
    standalone) echo '{"0":"audience-valkey:6379"}' ;;
    shadow)
      local names=(audience-valkey)
      for ((i=2; i<=n; i++)); do names+=("audience-valkey-$i"); done
      json_shards "${names[@]}"
      ;;
    cluster)
      local names=()
      for ((i=1; i<=n; i++)); do names+=("audience-cluster-$i"); done
      json_shards "${names[@]}"
      ;;
  esac
}
json_shards() {
  local out="{" i=0
  for name in "$@"; do
    (( i > 0 )) && out+=","
    out+="\"$i\":\"${name}:6379\""
    ((i++))
  done
  out+="}"
  echo "$out"
}

# fcap_shard_names / audience_shard_names print the container names for
# the given topology (for docker stats sampling).
fcap_shard_names() {
  local mode=$1 n=$2 prefix=perf-
  case "$mode" in
    standalone) echo "${prefix}fcap-valkey-1" ;;
    shadow)
      local out=("${prefix}fcap-valkey-1")
      for ((i=2; i<=n; i++)); do out+=("${prefix}fcap-valkey-${i}-1"); done
      echo "${out[*]}"
      ;;
    cluster)
      local out=()
      for ((i=1; i<=n; i++)); do out+=("${prefix}fcap-cluster-${i}-1"); done
      echo "${out[*]}"
      ;;
  esac
}
audience_shard_names() {
  local mode=$1 n=$2 prefix=perf-
  case "$mode" in
    standalone) echo "${prefix}audience-valkey-1" ;;
    shadow)
      local out=("${prefix}audience-valkey-1")
      for ((i=2; i<=n; i++)); do out+=("${prefix}audience-valkey-${i}-1"); done
      echo "${out[*]}"
      ;;
    cluster)
      local out=()
      for ((i=1; i<=n; i++)); do out+=("${prefix}audience-cluster-${i}-1"); done
      echo "${out[*]}"
      ;;
  esac
}

cluster_init() {
  # Form the fcap and audience clusters. `valkey-cli --cluster create` needs
  # the actual IPs (not container names) as returned by CLUSTER NODES; docker
  # embeds each container's IP into the compose network, so we resolve each
  # cluster peer by its docker-network-attached IP first.
  local -a fcap_addrs audience_addrs
  for i in $(seq 1 "$1"); do
    ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "perf-fcap-cluster-${i}-1")
    fcap_addrs+=("${ip}:6379")
    ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "perf-audience-cluster-${i}-1")
    audience_addrs+=("${ip}:6379")
  done
  echo "  forming fcap cluster: ${fcap_addrs[*]}"
  docker run --rm --network perf_default valkey/valkey:8.0-alpine \
    valkey-cli --cluster create "${fcap_addrs[@]}" --cluster-replicas 0 --cluster-yes
  echo "  forming audience cluster: ${audience_addrs[*]}"
  docker run --rm --network perf_default valkey/valkey:8.0-alpine \
    valkey-cli --cluster create "${audience_addrs[@]}" --cluster-replicas 0 --cluster-yes
  # `--cluster create` returns once gossip agrees, but the CLUSTER INFO
  # state can still be `loading` for a couple of seconds after — a client
  # that connects during that window sees CLUSTERDOWN. Poll on every node
  # until every one reports cluster_state:ok before proceeding.
  wait_cluster_ok "${fcap_addrs[@]}"
  wait_cluster_ok "${audience_addrs[@]}"
}

# wait_cluster_ok polls each cluster node's CLUSTER INFO until all report
# `cluster_state:ok`. Fails after 30s.
wait_cluster_ok() {
  local -a addrs=("$@")
  local deadline=$(( $(date +%s) + 30 ))
  while (( $(date +%s) < deadline )); do
    local all_ok=1
    for addr in "${addrs[@]}"; do
      local state
      state=$(docker run --rm --network perf_default valkey/valkey:8.0-alpine \
        valkey-cli -h "${addr%:*}" -p "${addr##*:}" cluster info 2>/dev/null \
        | awk -F: '/^cluster_state:/ {print $2}' | tr -d '\r\n ')
      if [[ "$state" != "ok" ]]; then
        all_ok=0
        break
      fi
    done
    if (( all_ok == 1 )); then
      echo "  cluster ok: ${addrs[*]}"
      return 0
    fi
    sleep 1
  done
  echo "!! cluster not stable within 30s: ${addrs[*]}" >&2
  return 1
}

# ---- output header --------------------------------------------------------
echo "topology,mode,shards,target_qps,concurrency,write_qps_fcap,write_qps_audience,achieved_qps,p50_latency_ms,p90_latency_ms,p99_latency_ms,p999_latency_ms,ok_2xx,non_2xx,errors,qps_per_shard,identity_cpu_peak_pct,fcap_valkey_cpu_peak_pct,fcap_valkey_rss_peak_mb,audience_valkey_cpu_peak_pct,audience_valkey_rss_peak_mb" > "$SUMMARY_CSV"

# ---- build once -----------------------------------------------------------
echo "==> building images"
for svc in identity-agent configserver seeder loadgen writer; do
  docker compose build "$svc"
done

# ---- per-topology loop ---------------------------------------------------
for topo_line in "${TOPOLOGIES[@]}"; do
  IFS='|' read -r topo_name mode nshards profiles <<< "$topo_line"
  echo
  echo "==================================================================="
  echo "TOPOLOGY=$topo_name  mode=$mode  shards=$nshards  profiles=$profiles"
  echo "==================================================================="

  topo_dir="$RESULTS_DIR/$topo_name"
  mkdir -p "$topo_dir"

  # Compose args for this topology.
  compose_args=(-f docker-compose.yml)
  IFS=',' read -r -a profile_list <<< "$profiles"
  for p in "${profile_list[@]}"; do
    [[ -n "$p" ]] && compose_args+=(--profile "$p")
  done

  # SHARDS JSON envs — identity-agent AND seeder both read these.
  export FCAP_VALKEY_MODE="$mode"
  export AUDIENCE_VALKEY_MODE="$mode"
  export FCAP_VALKEY_SHARDS="$(fcap_shard_json "$mode" "$nshards")"
  export AUDIENCE_VALKEY_SHARDS="$(audience_shard_json "$mode" "$nshards")"

  # Tear down anything from a previous topology so container IDs are clean.
  docker compose --profile shadow --profile cluster --profile cluster-large down -v --remove-orphans >/dev/null 2>&1 || true

  # Bring up shards + config.
  echo "  starting valkeys + configserver"
  # Start every valkey the topology needs by name so profile-guarded ones fire.
  valkey_services=()
  case "$mode" in
    standalone)
      valkey_services=(fcap-valkey audience-valkey)
      ;;
    shadow)
      valkey_services=(fcap-valkey audience-valkey)
      for ((i=2; i<=nshards; i++)); do valkey_services+=("fcap-valkey-$i" "audience-valkey-$i"); done
      ;;
    cluster)
      for ((i=1; i<=nshards; i++)); do valkey_services+=("fcap-cluster-$i" "audience-cluster-$i"); done
      ;;
  esac
  IDENTITY_CPUS="$IDENTITY_CPUS" IDENTITY_MEMORY="$IDENTITY_MEMORY" \
    docker compose "${compose_args[@]}" up -d configserver "${valkey_services[@]}"

  if [[ "$mode" == "cluster" ]]; then
    echo "  waiting for cluster nodes to accept connections"
    sleep 3
    cluster_init "$nshards"
  fi

  echo "  starting identity-agent"
  IDENTITY_CPUS="$IDENTITY_CPUS" IDENTITY_MEMORY="$IDENTITY_MEMORY" \
    docker compose "${compose_args[@]}" up -d --force-recreate identity-agent

  identity_cid=$(docker compose ps -q identity-agent)
  docker update --cpus "$IDENTITY_CPUS" --memory "$IDENTITY_MEMORY" --memory-swap "$IDENTITY_MEMORY" \
    "$identity_cid" >/dev/null
  want_nanocpus=$(awk -v c="$IDENTITY_CPUS" 'BEGIN{printf "%d", c*1000000000}')
  got_nanocpus=$(docker inspect --format '{{.HostConfig.NanoCpus}}' "$identity_cid")
  if [[ "$got_nanocpus" != "$want_nanocpus" ]]; then
    echo "!! CPU limit not applied: want $want_nanocpus got $got_nanocpus" >&2
    exit 1
  fi

  wait_healthy 120 || { echo "!! identity-agent unhealthy"; docker compose logs --tail=100 identity-agent; continue; }

  echo "  seeding across $nshards $mode shard(s)"
  IDENTITY_CPUS="$IDENTITY_CPUS" IDENTITY_MEMORY="$IDENTITY_MEMORY" \
    docker compose "${compose_args[@]}" run --rm seeder >"$topo_dir/seeder.log" 2>&1 || {
    echo "!! seeder failed"; cat "$topo_dir/seeder.log" >&2; continue;
  }

  # ---- qps ramp -----
  fcap_names=($(fcap_shard_names "$mode" "$nshards"))
  audience_names=($(audience_shard_names "$mode" "$nshards"))
  all_sampled=("$identity_cid" "${fcap_names[@]}" "${audience_names[@]}")

  # Optional background writer — mixed read+write workload. Sized to cover
  # the whole topology's QPS ramp with headroom (writer self-exits on
  # DURATION timeout).
  writer_cid=""
  if (( WRITE_QPS_FCAP > 0 || WRITE_QPS_AUDIENCE > 0 )); then
    total_secs=$(( ${#QPS_STEPS[@]} * ( $(_dur_seconds "$DURATION") + $(_dur_seconds "$WARMUP") + 15 ) ))
    echo "  starting writer: fcap=${WRITE_QPS_FCAP}qps audience=${WRITE_QPS_AUDIENCE}qps for ${total_secs}s"
    WRITER_DURATION="${total_secs}s" \
    IDENTITY_CPUS="$IDENTITY_CPUS" IDENTITY_MEMORY="$IDENTITY_MEMORY" \
      docker compose "${compose_args[@]}" up -d writer
    writer_cid=$(docker compose ps -q writer)
    # Include writer in stats sampling so we can tell it apart from
    # identity-agent's contribution to Valkey load.
    all_sampled+=("$writer_cid")
    # Give the writer a moment to spool up before the first loadgen step so
    # the warmup phase sees write load in effect too.
    sleep 2
  fi

  for qps in "${QPS_STEPS[@]}"; do
    label="${topo_name}_${qps}qps"
    report_path="/results/${topo_name}_${qps}qps.json"
    host_report="$RESULTS_DIR/${topo_name}_${qps}qps.json"
    echo "  -> qps=$qps duration=$DURATION concurrency=$CONCURRENCY"

    stats_log="$topo_dir/stats_${qps}qps.log"
    : > "$stats_log"
    (
      while true; do
        sample_stats "${all_sampled[@]}" >> "$stats_log" 2>/dev/null || true
        sleep 1
      done
    ) &
    sampler_pid=$!
    trap 'kill '"$sampler_pid"' 2>/dev/null || true' EXIT INT TERM

    IDENTITY_CPUS="$IDENTITY_CPUS" IDENTITY_MEMORY="$IDENTITY_MEMORY" \
    QPS="$qps" DURATION="$DURATION" WARMUP="$WARMUP" CONCURRENCY="$CONCURRENCY" \
      LABEL="$label" REPORT="$report_path" \
      docker compose "${compose_args[@]}" run --rm loadgen 2>&1 | tee "$topo_dir/loadgen_${qps}qps.log" || true

    kill "$sampler_pid" 2>/dev/null || true
    wait "$sampler_pid" 2>/dev/null || true
    trap - EXIT INT TERM

    if [[ -f "$host_report" ]]; then
      # Peak identity-agent CPU + aggregate Valkey peaks (max over all shards of
      # a given backend — that's the shard hottest under load).
      id_cpu=$(peak_from_stats "$stats_log" "perf-identity-agent" 3)
      fcap_cpu=$(peak_from_stats "$stats_log" "perf-fcap-" 3)
      fcap_rss=$(peak_from_stats "$stats_log" "perf-fcap-" 2)
      aud_cpu=$(peak_from_stats "$stats_log" "perf-audience-" 3)
      aud_rss=$(peak_from_stats "$stats_log" "perf-audience-" 2)
      python3 - "$host_report" "$topo_name" "$mode" "$nshards" "$CONCURRENCY" \
        "$WRITE_QPS_FCAP" "$WRITE_QPS_AUDIENCE" \
        "$id_cpu" "$fcap_cpu" "$fcap_rss" "$aud_cpu" "$aud_rss" >> "$SUMMARY_CSV" <<'PY'
import json, sys
p, topo, mode, shards, conc, wq_fcap, wq_aud, id_cpu, fc_cpu, fc_rss, aud_cpu, aud_rss = sys.argv[1:]
r = json.load(open(p))
achieved = r.get("achieved_qps", 0)
row = [
    topo, mode, shards, r.get("target_qps",""), conc,
    wq_fcap, wq_aud,
    f"{achieved:.1f}",
    f"{r.get('p50_ms',0):.2f}",
    f"{r.get('p90_ms',0):.2f}",
    f"{r.get('p99_ms',0):.2f}",
    f"{r.get('p99_9_ms',0):.2f}",
    r.get("ok_2xx",0), r.get("non_2xx",0), r.get("transport_errors",0),
    f"{(achieved / max(float(shards), 1e-9)):.1f}",
    id_cpu, fc_cpu, fc_rss, aud_cpu, aud_rss,
]
print(",".join(str(c) for c in row))
PY
    fi
  done

  if [[ -n "$writer_cid" ]]; then
    docker compose "${compose_args[@]}" stop writer >/dev/null 2>&1 || true
  fi
  echo "  tearing down topology $topo_name"
  docker compose "${compose_args[@]}" down -v --remove-orphans >/dev/null
done

echo
echo "=== done ==="
echo "results: $RESULTS_DIR"
echo "summary: $SUMMARY_CSV"
if command -v column >/dev/null 2>&1; then column -t -s , "$SUMMARY_CSV"; else cat "$SUMMARY_CSV"; fi
