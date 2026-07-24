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
verification defaults to off (`TMP_ALLOW_UNSIGNED=true`) so the load
generator can emit plain `ContextMatchRequest` JSON;
`SIGN_REQUESTS=true ./run.sh` flips the whole matrix to signed mode
(see below).

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

# Signed-mode sweep — same matrix, X-AdCP-Signature on every request.
# Brings up a mock tmpregistry service, enforces TMP_ALLOW_UNSIGNED=false
# on the agent, and stamps the CSV rows with `signed=signed`.
SIGN_REQUESTS=true ./run.sh packages-only
```

Any of the following can be overridden via env:
`DURATION` (default `30s`), `WARMUP` (`5s`), `CONCURRENCY` (`256`),
`RESULTS_DIR`.

## Signed-mode sweep

`SIGN_REQUESTS=true` runs the whole scenario × config matrix with
Ed25519-signed requests so ops can directly compare "with signing" vs
"without" at the same load point. What changes when the flag is set:

- The `tmpregistry` compose service comes up under `--profile signed` and
  publishes an Ed25519 public JWK at `GET /registry/snapshot` (single
  property, single key). The keypair is generated on first boot into a
  shared `bench-signer-keys` volume so subsequent restarts stay stable.
- The context-agent runs with `TMP_ALLOW_UNSIGNED=false`,
  `TMP_REGISTRY_URL=http://tmpregistry:9002/registry/snapshot`,
  `TMP_REGISTRY_ALLOW_INSECURE_SCHEME=true` (the compose network makes TLS
  unnecessary and self-signed certs a distraction).
- The loadgen loads the shared private key, builds a `tmproto.Signer`,
  and stamps `X-AdCP-Signature` / `X-AdCP-Key-Id` on every request. The
  provider_endpoint_url in the signing input matches the agent's
  `TMP_OWN_ENDPOINT_URL` so verification succeeds.
- The CSV `signed` column is `signed` on such runs and `unsigned`
  otherwise.

Context-match uses newline-joined string signing (much cheaper than
identity-match's JCS+SHA-256), so the expected delta is smaller than
the identity harness — dominated by Ed25519 verify itself.

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

- TMP signature verification is off by default so the baseline scenarios
  measure the handler + Valkey path in isolation. Set `SIGN_REQUESTS=true`
  to include Ed25519 verify in the measured cost; see the "Signed-mode
  sweep" section above for what changes.
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

## Reference results (2026-07-23, main)

Full-matrix sweep of `./run.sh` on a clean checkout of the branch this
harness landed on (`bench/context-perf/` committed at `346df5a`).
Sweep = 3 scenarios × 7 CPU/mem configs × 7 QPS steps = 147 runs.
Wall time ~90 min.

**Hardware / OS:** same host as identity-perf — AMD EPYC 9254 (24 c /
48 t, 4.15 GHz max), 384 GiB DDR5, Ubuntu 24.04.4, Docker 29.1.3
(compose v2.40.3), bare metal.

### Saturation ceiling per CPU allocation

Achieved QPS at target=32,000. `_2g` / `_4g` / `_8g` variants of each
CPU count matched to within noise — memory is not the constraint on
any scenario.

| CPUs | packages-only | packages-topics | packages-signals | context-agent CPU peak |
|-----:|--------------:|----------------:|-----------------:|-----------------------:|
| 1    | 1,929         | 1,406           | 1,094            | 101–104% (cgroup ceiling) |
| 2    | 4,090         | 2,803           | 2,244            | 200% |
| 4    | 6,132         | 4,266           | 3,763            | 395–399% |
| 8    | 6,652         | 4,972           | 5,103            | 700 / 723 / 770% (near-saturation on topics + signals; some headroom on packages-only) |

Errors across the whole 147-row matrix: **0 transport errors** and
**≤ 11 non-2xx per 32k-target run** (total 79 non-2xx across ~15 M
requests — all 1-CPU packages-only + packages-topics rows hitting the
1 s `REQUEST_TIMEOUT` under queue pressure). Every other row has
`non_2xx = 0`.

### qps-per-core

| CPUs | packages-only | packages-topics | packages-signals |
|-----:|--------------:|----------------:|-----------------:|
| 1    | 1,929         | 1,406           | 1,094            |
| 2    | 2,045         | 1,402           | 1,122            |
| 4    | 1,533         | 1,067           | 941              |
| 8    | 832           | 622             | 638              |

Per-core efficiency peaks at **2 CPUs** (same shape as identity-agent).
Real diminishing returns past 4 CPU on every scenario. 8 CPU
packages-only is ~87 % of cgroup — some headroom but achieved_qps flat
across 8k / 16k / 32k targets → work coming from the loadgen isn't the
limit.

### Cost relative to identity-agent

context-agent is roughly **3–4× more expensive per core** than
identity-agent at comparable CPU allocations (identity-agent 1 c
fcap-only = 7,222 qps; context-agent 1 c packages-only = 1,929 qps).
Expected: identity-agent does 1 fcap MGet + 1 audience HEXISTS per
request against ~10 pkgs, whereas context-agent evaluates 200 active
packages per request including pkgconfig MGets, per-artifact topic /
signal fan-out, and suppression checks.

### Memory

context-agent container RSS: **37–100 MB** across the entire matrix,
scenario-independent. Valkey RSS: 4.6–7.9 MB (dominated by the seeded
corpus, not by request load). Memory cap doesn't influence throughput
at any config; **512 MB is enough, 1 GB is comfortable**.

### Latency at saturation (target 32 k qps, packages-signals)

| CPUs | p50 | p99   | p99.9 |
|-----:|----:|------:|------:|
| 1    | 217 ms | 367 ms | 405 ms |
| 2    | 113 ms | 163 ms | 188 ms |
| 4    |  65 ms | 138 ms | 202 ms |
| 8    |  49 ms | 111 ms | 138 ms |

Tail is dominated by queueing at the 1 s `REQUEST_TIMEOUT`; the
underlying handler latency at low load is ~2 ms p50 (see the 8k rows
in the CSV before saturation kicks in).

Raw `summary.csv` and per-step JSON reports are archived on the
benchmark host under `bench/context-perf/results/main-20260723T190414Z/`.

## Signed-mode reference (2026-07-24)

Smoke sweep at `4c/4g packages-only` on the same hardware as above,
comparing baseline unsigned against `SIGN_REQUESTS=true`. Both runs on
the AI-4641 branch. Full 7-step QPS ladder each; only the saturation
steps shown — at low QPS the two are indistinguishable within noise.

| target QPS | unsigned achieved | signed achieved | unsigned p99 | signed p99 | qps/core (unsigned → signed) |
|-----------:|------------------:|----------------:|-------------:|-----------:|-----------------------------:|
|  4,000     |  3,996            |  3,998          |   2.81 ms    |   5.14 ms  | 999 → 999 (~0%)              |
|  8,000     |  6,088            |  5,805          | 124.87 ms    | 153.93 ms  | 1,522 → 1,451 (-5%)          |
| 16,000     |  6,100            |  5,771          | 123.01 ms    | 152.63 ms  | 1,525 → 1,443 (-5%)          |
| 32,000     |  6,092            |  5,791          | 124.78 ms    | 153.38 ms  | **1,523 → 1,448 (-5%)**      |

context-agent CPU peak sat at ~395 % in both runs. `ok_2xx == total`
on every step; signature verification passed on 100 % of the 174k
signed requests at target 32k qps.

The **-5 % qps-per-core** drop is much smaller than identity-perf's
-36 % because context-match signing uses newline-joined string signing
(no JCS canonicalization), so the added per-request cost is dominated
by Ed25519 verify itself — ~34 μs / req derived from the qps-per-core
delta (656 μs CPU / req unsigned → 690 μs signed on 4 cores). The
+28 ms p99 shift is tail-latency jitter at an already-saturated
handler + Valkey ceiling, not incremental crypto load.
