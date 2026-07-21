# identity-agent perf benchmark

Self-contained docker-compose stack that measures how many QPS-per-core the
identity-agent handles, and how much RAM it needs, across a matrix of CPU
and memory caps. Two scenarios are supported out of the box:

- `fcap-only` — frequency capping only; audience Valkey is not exercised.
- `fcap-audience` — frequency capping plus audience-membership targeting.

No external services or auth are required — the stack ships its own Valkey
instances and a mock CONFIG_SOURCE server that the identity-agent polls for
its package snapshot. TMP signature verification is turned off
(`TMP_ALLOW_UNSIGNED=true`) so the load generator can emit plain
`IdentityMatchRequest` JSON.

## Stack

```
                     +--------------------+
                     |    configserver    |   POST /v1/identity-configs
                     |  (mock CONFIG_SRC) |     -> synthetic package snapshot
                     +---------+----------+
                               |
                               v
+----------+   HEXISTS   +-----+------+   HEXISTS   +---------------+
| loadgen  |------------>| identity-  |<-----------+  audience-     |
|(one-shot)|             |  agent     |             |  valkey       |
+----------+   POST      |            |             +---------------+
                         |            |
                         |            |             +---------------+
                         |            |<-----------+  fcap-valkey  |
                         +------------+   HEXISTS   +---------------+
```

## Prerequisites

- Linux host (bare metal). Tested with Ubuntu 22.04+.
- Docker 24+ with `docker compose` v2.
- `python3` (used to roll per-run JSON reports into the summary CSV).

## Quick start

```
# Clone the repo on the target server, then:
cd bench/perf
./run.sh
```

The full matrix (7 CPU/memory configurations × 2 scenarios × 7 QPS steps)
takes roughly 90 minutes at the defaults. Each run writes a report under
`bench/perf/results/<UTC-timestamp>/` and appends a row to `summary.csv`
in the same directory.

## Subsets

```
# Only fcap-only, full CPU/memory matrix:
./run.sh fcap-only

# Single scenario at a specific cap:
./run.sh fcap-audience 4 8g

# Custom QPS points and duration:
QPS_STEPS="1000 5000 10000" DURATION=60s ./run.sh fcap-only 4 4g
```

Any of the following can be overridden via env:
`DURATION` (default `30s`), `WARMUP` (`5s`), `CONCURRENCY` (`256`),
`RESULTS_DIR`.

Scenario-level overrides come from `scenarios/*.env`; edit those to change
the seeded corpus (users, audiences, capped fraction) or the request shape
(packages per request, identities per request).

## Configurations swept

CPU / memory caps applied to the identity-agent container via
`deploy.resources.limits`:

| CPUs | Memory |
|-----:|-------:|
|   1  |   1 GB |
|   1  |   2 GB |
|   2  |   2 GB |
|   2  |   4 GB |
|   4  |   4 GB |
|   4  |   8 GB |
|   8  |   8 GB |

Edit the `CONFIGS` array at the top of `run.sh` to tweak the sweep.

## What is measured

Per run, `loadgen` reports:

- `achieved_qps` — actually-served RPS over the measurement window.
- `p50_ms`, `p90_ms`, `p99_ms`, `p99_9_ms`, `max_ms`, `mean_ms` — request latency
  (client-observed, includes network+handler+Valkey). **All values are
  milliseconds.**
- `ok_2xx`, `non_2xx`, `transport_errors`.
- `status_codes` histogram.

Summary CSV columns:

| Column | Unit |
|---|---|
| `achieved_qps`, `target_qps`, `qps_per_core` | requests / second |
| `p50_latency_ms`, `p90_latency_ms`, `p99_latency_ms`, `p999_latency_ms` | milliseconds |
| `identity_rss_peak_mb`, `audience_valkey_rss_peak_mb`, `fcap_valkey_rss_peak_mb` | megabytes (peak container RSS during the run) |
| `identity_cpu_peak_pct`, `audience_valkey_cpu_peak_pct`, `fcap_valkey_cpu_peak_pct` | percent (docker-cgroup CPU; 100% == 1 core, so 400% == 4 cores fully used) |
| `memory_gb` | identity-agent container memory cap, gigabytes |

The full 1 Hz stats time series for each run is preserved at
`<scenario>_<cpu>c_<mem>/stats_<qps>qps.log` — three lines per second
(`<container-name> <rss_mb> <cpu_pct>`) — so you can plot the shape, not
just the peak.

The runner also captures:

- `metrics_<qps>qps.prom` — full `/metrics` snapshot at the end of each step.
- `rss_<qps>qps.log` — 1 Hz samples of the container's memory usage; the peak
  is rolled into the summary CSV as `rss_mb_peak`.
- `qps_per_core` — `achieved_qps / cpus`, the headline number for
  capacity-planning.

## Turning the knobs

To exercise a different corpus size or request shape without touching code,
edit the relevant `scenarios/*.env`:

- `TOTAL_USERS` — how many distinct user tokens the seeder populates and
  loadgen draws from.
- `FCAP_USER_FRACTION` — fraction of users that carry any fcap markers.
- `PACKAGES_CAPPED_PER_USER` — how many packages each capped user hits.
- `AUDIENCE_PACKAGES` — how many packages carry an audience rule.
- `AUDIENCES_PER_PACKAGE` — anyOf list length on each such package.
- `PACKAGES_PER_REQ` — package_ids on each identity-match request.
- `IDENTITIES_PER_REQ` — identity tokens per request (1..3, per TMP schema).

## Manual invocation

If you want to drive one particular step without the sweep:

```
docker compose build
IDENTITY_CPUS=2 IDENTITY_MEMORY=2g docker compose up -d
docker compose run --rm seeder
QPS=5000 DURATION=60s CONCURRENCY=256 REPORT=/results/manual.json \
  docker compose run --rm loadgen
```

The identity-agent's `/metrics` is available on the compose network at
`http://identity-agent:8080/metrics`. Set `HOST_METRICS_PORT` (default
9464) to expose it on the host for an external Prometheus scraper.

## Notes / caveats

- TMP signature verification is off. Enabling it in this stack would
  require standing up a signing key server; not useful for a raw
  throughput measurement of the handler + Valkey path.
- The mock config server returns a static snapshot. Refresh churn is
  therefore not part of the measurement — reasonable because refresh is a
  background goroutine on a 5-minute cadence in production.
- Both Valkeys run with `--maxmemory-policy allkeys-lru` and no
  persistence, so seeded data stays hot across runs but doesn't survive a
  compose down. `run.sh` re-seeds on every CPU/memory config change.
- `MAX_OPEN_CONNECTIONS` on the agent is raised to 4096 (from the default
  1024). Raise it further if the loadgen's `concurrency` exceeds this.
