# context-agent perf benchmark

Self-contained docker-compose stack that measures how many QPS-per-core
the context-agent handles, and how much RAM it needs, across a matrix
of CPU and memory caps. Three scenarios are supported out of the box:

- `packages-only` — active-package lookup only (mediabuystore +
  pkgconfigstore MGet); no artifact_refs, no topics, no signals.
- `packages-topics` — packages + artifact_refs → topic-set intersection
  (exercises the topicstore SetMembers path).
- `packages-signals` — packages + context-signal targeting (exercises
  the signalstore MGet path against `signal:{owner}:{key_types}:{values}`).

No external services or auth are required — the stack ships its own
Valkey instance and the seeder pre-populates every read path through
the same store packages the context-agent reads through. TMP signature
verification is turned off (`TMP_ALLOW_UNSIGNED=true`) so the load
generator can emit plain `ContextMatchRequest` JSON.

## Stack

```
+----------+   POST      +----------------+   MGet / SetMembers   +--------+
| loadgen  |----------->|  context-agent |---------------------->| valkey |
|(one-shot)|             |    (SUT)       |                       +--------+
+----------+             +----------------+                            ^
                                                                       |
                                       one-shot HSET / SET / SADD      |
                                       +-----------+                   |
                                       |  seeder   |-------------------+
                                       +-----------+
```

## Prerequisites

- Linux host (bare metal). Tested with Ubuntu 22.04+.
- Docker 24+ with `docker compose` v2.
- `python3` (used to roll per-run JSON reports into the summary CSV).

## Quick start

```
cd bench/context-perf
./run.sh
```

The full matrix (7 CPU/memory configurations × 3 scenarios × 7 QPS
steps) takes on the order of two hours at the defaults. Each run writes
a report under `bench/context-perf/results/<UTC-timestamp>/` and
appends a row to `summary.csv` in the same directory.

## Subsets

```
# Only packages-only, full CPU/memory matrix:
./run.sh packages-only

# Single scenario at a specific cap:
./run.sh packages-topics 4 8g

# Custom QPS points and duration:
QPS_STEPS="1000 5000 10000" DURATION=60s ./run.sh packages-only 4 4g
```

Any of the following can be overridden via env:
`DURATION` (default `30s`), `WARMUP` (`5s`), `CONCURRENCY` (`256`),
`RESULTS_DIR`.

Scenario-level overrides come from `scenarios/*.env`; edit those to
change the seeded corpus (packages, topics, signals) or the request
shape (packages / artifact_refs per request).

## Configurations swept

CPU / memory caps applied to the context-agent container:

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
- `p50_ms`, `p90_ms`, `p99_ms`, `p99_9_ms`, `max_ms`, `mean_ms` —
  request latency (client-observed, includes network + handler +
  Valkey). **All values are milliseconds.**
- `ok_2xx`, `non_2xx`, `transport_errors`.
- `status_codes` histogram.

Summary CSV columns:

| Column | Unit |
|---|---|
| `achieved_qps`, `target_qps`, `qps_per_core` | requests / second |
| `p50_latency_ms`, `p90_latency_ms`, `p99_latency_ms`, `p999_latency_ms` | milliseconds |
| `context_rss_peak_mb`, `valkey_rss_peak_mb` | megabytes (peak container RSS during the run) |
| `context_cpu_peak_pct`, `valkey_cpu_peak_pct` | percent (docker-cgroup CPU; 100% == 1 core) |
| `memory_gb` | context-agent container memory cap |

The full 1 Hz stats time series for each run is preserved at
`<scenario>_<cpu>c_<mem>/stats_<qps>qps.log` — two lines per second
(`<container-name> <rss_mb> <cpu_pct>`) — so you can plot the shape,
not just the peak.

The runner also captures `metrics_<qps>qps.prom` — a full `/metrics`
snapshot at the end of each step — via the admin port
(`ADMIN_PORT=8082` inside the container, host `HOST_METRICS_PORT`
default `9465`). The request port (`HTTP_PORT=8081` inside, host
`HOST_HTTP_PORT` default `9464`) is also published so `wait_healthy`
can poll `/health` (which is mounted only on the request mux) and
operators can `curl` `/context` from the host for one-off debugging.
`qps_per_core = achieved_qps / cpus` is the headline capacity-planning
number.

## Turning the knobs

Edit the relevant `scenarios/*.env` to change the seeded corpus or
request shape without touching code:

- `TOTAL_PACKAGES` — how many active packages the seeder writes.
- `TOTAL_ARTIFACTS` — how many artifact URLs live in the loadgen's
  pool.
- `TOPIC_TARGETS_ENABLED` — set `TopicTargets=true` on every seeded
  PackageContextConfig and seed both per-package and per-artifact
  topic sets.
- `TOTAL_TOPICS`, `TOPICS_PER_PACKAGE`, `TOPICS_PER_ARTIFACT` — shape
  the taxonomy corpus. Every package AND every artifact carries a
  shared always-on topic (index 0) so intersection is guaranteed
  non-empty; the remaining slots draw from `1..TOTAL_TOPICS-1`.
- `SIGNALS_ENABLED` — attach an `any_of` Cfg to every package under
  the fixed `url_hash` key_type and seed one `signal:*` key per URL
  in the loadgen's `TOTAL_ARTIFACTS` pool.
- `PACKAGES_PER_REQ` — 0 (default) evaluates against every active
  package for the seller; >0 restricts to a random subset.
- `ARTIFACT_REFS_PER_REQ` — how many `url_hash` artifact refs the
  loadgen puts on each request. Drives topic-set / signal-key MGet
  fan-out.

## Manual invocation

If you want to drive one particular step without the sweep:

```
docker compose build
CONTEXT_CPUS=2 CONTEXT_MEMORY=2g docker compose up -d
TOPIC_TARGETS_ENABLED=true docker compose run --rm seeder
QPS=5000 DURATION=60s CONCURRENCY=256 REPORT=/results/manual.json \
  ARTIFACT_REFS_PER_REQ=3 \
  docker compose run --rm loadgen
```

The context-agent's `/metrics` is on the compose network at
`http://context-agent:8082/metrics`. Set `HOST_METRICS_PORT` (default
`9465`) to expose it on the host for an external Prometheus scraper.

## Notes / caveats

- TMP signature verification is off. Enabling it in this stack would
  require standing up a signing key server; not useful for a raw
  throughput measurement of the handler + Valkey path.
- Unlike the identity-agent harness (`bench/identity-perf`), there is no
  configserver: the context-agent has no polling snapshot source.
  Every read hits Valkey — optionally fronted by the per-domain LRU
  caches (`CACHE_ENABLED=true` in the compose, matching production).
- The seeder writes through `mediabuystore.Service`,
  `pkgconfigstore.Service`, `topicstore.Writer`, and the signal
  `signalstore.Key` helper so key formats stay in lockstep with what
  the context-agent reads. There is no hand-rolled key construction.
- Valkey runs with `--maxmemory-policy allkeys-lru` and no
  persistence, so seeded data stays hot across runs but doesn't
  survive a compose down. `run.sh` re-seeds on every CPU/memory
  config change.
- No baseline suppressions are seeded. Property/geo suppression would
  short-circuit every request; if you want to measure the fail-closed
  path, add a suppression seed step invoking
  `suppressionstore.NewService(store).SuppressProperty(...)`.
- `MAX_OPEN_CONNECTIONS` on the agent is raised to 4096 (from the
  default 1024). Raise it further if the loadgen's `CONCURRENCY`
  exceeds this.
- `REQUEST_TIMEOUT` is set to `1s` (production example.env uses 40ms).
  Deliberately generous so a saturation sweep measures the SUT's real
  tail latency instead of clipping p99 at the deadline.
- `PROPERTY_RIDS` is defined once in `bench/context-perf/perf.env`
  and loaded into every service that needs it via `env_file:` in the
  compose file. Edit the list there — the seeder, loadgen, and
  context-agent all pick it up automatically (Go-side fallback in
  `internal/corpus` applies only when the env var is unset, e.g.
  local dev / tests).
