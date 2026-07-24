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
cd bench/identity-perf
./run.sh
```

The full matrix (7 CPU/memory configurations × 2 scenarios × 7 QPS steps)
takes roughly 90 minutes at the defaults. Each run writes a report under
`bench/identity-perf/results/<UTC-timestamp>/` and appends a row to `summary.csv`
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
- `IDENTITIES_PER_REQ` — identity tokens per request. **Gated to `1`
  today** — only MAID-shaped tokens survive the identity-agent's
  canonicalizer end-to-end with the seeder's current key format. See
  the comment in `cmd/loadgen/main.go` around `identitiesPerReq`.
  Multi-identity sweeps would silently read cold keys.

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

## Reference results (2026-07-23, main)

Full-matrix sweep of `./run.sh` on a clean `origin/main` checkout at
commit `775dbae`. Sweep = 2 scenarios × 7 CPU/mem configs × 7 QPS steps
= 98 runs. Wall time ~50 min.

**Hardware / OS:**

| | |
|---|---|
| CPU | AMD EPYC 9254 (24 cores / 48 threads, 4.15 GHz max) |
| RAM | 384 GiB (DDR5) |
| OS | Ubuntu 24.04.4 LTS (kernel 6.8.0-134-generic) |
| Docker | 29.1.3 (compose v2.40.3) |
| Host | Latitude bare metal, no neighbor workload (load avg ~0.5) |

### Saturation ceiling per CPU allocation

Achieved QPS at target=32,000 (loadgen ticker becomes the bottleneck
past that). `_2g` and `_4g` variants of each CPU count matched to
within noise — memory is not the constraint.

| CPUs | fcap-only achieved | fcap-audience achieved | identity-agent CPU peak |
|-----:|-------------------:|-----------------------:|------------------------:|
| 1    |  7,222             |  4,693                 | 101% (cgroup ceiling)   |
| 2    | 15,092             |  9,411                 | 201% / 202%             |
| 4    | 25,737             | 17,023                 | 399% / 402%             |
| 8    | 31,899             | 28,313                 | 570% / 775% (has headroom) |

`ok_2xx == total` on every one of the 98 rows; `non_2xx = errors = 0`
across the full matrix.

### qps-per-core

| CPUs | fcap-only | fcap-audience |
|-----:|----------:|--------------:|
| 1    | 7,222     | 4,693         |
| 2    | 7,546     | 4,706         |
| 4    | 6,434     | 4,256         |
| 8    | 3,987     | 3,539         |

Per-core efficiency peaks at **2 CPUs**. Diminishing returns start at
4; 8 CPU is loadgen-bound before it saturates the SUT (identity CPU
peak 570% on fcap-only and 775% on fcap-audience — the container has
2-3 cores of headroom).

### Memory

Identity-agent container RSS stays between **13 MB and 68 MB** across
the entire matrix. Memory cap doesn't influence throughput at any
config; **512 MB is enough, 1 GB is comfortable**.

Audience-Valkey and fcap-Valkey RSS: 4-5 MB and 37-39 MB respectively
across the sweep — dominated by the seeded key set, not by request
load.

### Latency at saturation (target 32k qps, fcap-audience)

| CPUs | p50 | p99  | p99.9 |
|-----:|----:|-----:|------:|
| 1    | 60 ms  | 105 ms | 147 ms |
| 2    | 27 ms  |  43 ms |  49 ms |
| 4    | 15 ms  |  24 ms |  28 ms |
| 8    |  9 ms  |  14 ms |  18 ms |

### Cross-check with PR #413 baseline

PR #413 (merged 2026-07-22, commit before the post-#413 identityagent
follow-ups) recorded these `achieved_qps` at target=32k on the same
CPU model:

| CPUs | fcap-only #413 → main | fcap-audience #413 → main |
|-----:|----------------------:|--------------------------:|
| 1    | 8,300 → 7,222 (**-13%**) | 6,500 → 4,693 (**-28%**) |
| 2    | 19,300 → 15,092 (**-22%**) | 15,000 → 9,411 (**-37%**) |
| 4    | 31,900 → 25,737 (**-19%**) | 26,700 → 17,023 (**-36%**) |
| 8    | 31,900 → 31,899 (0%) | 31,900 → 28,313 (-11%) |

**A consistent 15-25% regression on the fcap-only path and 30-40% on
the fcap-audience path at CPU-bound configs, on identical hardware,
between #413 baseline and current main.** The 8c fcap-only number
matches exactly (both are loadgen-ticker-bound). Non-2xx and errors
are 0 in both datasets, so this isn't a correctness change reducing
work — it's per-request cost going up. Post-#413 commits that touched
the request path are the natural suspects (notably `3f8d1a6`
identityagent audience fail-closed on store error and `103cbdb`
union-read shadow-mode fallback); worth bisecting.

Follow-up ticket to bisect the regression: TBD.

Raw `summary.csv` and per-step JSON reports are archived on the
benchmark host under `bench/perf/results/main-20260723T174449Z/`.
