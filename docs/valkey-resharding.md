# Valkey resharding runbook

How to grow (or shrink) the number of shards on the identity-agent's fcap or
audience Valkey backends without losing reads mid-migration.

## Why this needs a runbook at all

The identity-agent uses [shadow-shards mode][shadow-doc] against its Valkey
backends: reads route via app-level CRC16 to N standalone shadow replicas
that mirror a central primary cluster. Writes go to the primary; readers
fan out across in-region shadows.

The slot-to-shard mapping the reader uses is
`clusterslot.NewShardMap(N).Shard(key)` — **N is captured at process start
and can't change without a restart**. So a straight-through resharding of
the primary cluster (e.g., `valkey-cli --cluster add-node` + `--cluster
reshard`) has a problem: the moment N changes in the reader's config,
CRC16 slots renumber, and a fraction of the keyspace lands on the "wrong"
shadow relative to where the underlying data physically lives.

This runbook uses the **union-read fallback** shipped with the
identity-agent to mask that window. During the migration the reader is
told about both the pre-migration topology (fallback) and the post-migration
topology (primary). It ORs `HEXISTS` results across the two; a `true` from
either side wins. Fcap "capped" and audience "member" are positive-truth
predicates, so the union is safe throughout the timeline — pre-, mid-, and
post-reshard.

**Read this if you're growing the audience backend during an active
consent-withdrawal or opt-out event.** Audience membership is not
monotone: `audience.Upsert` supports `Remove`, and a removal only starts
returning `false` from the OR once the underlying DEL has propagated to
BOTH shadows. During the union window an already-removed user can still
read `true` for the up-to-`replication-lag`-plus-`fallback-window`
interval. For `anyOf` inclusion segments this over-targets a removed
user; for `noneOf` exclusion segments it over-excludes them. If your
audience carries consent-withdrawal or right-to-be-forgotten semantics
with strict SLAs, delay non-urgent reshards until the removal wave has
drained on both shadows, or schedule the reshard during a low-removal
window. Bound the extra latency:
`max(shadow-0 replication lag, shadow-1 replication lag)` under the
resharding load.

[shadow-doc]: ../targeting/redisstore/store.go

## What the fallback is (and isn't)

`FCAP_FALLBACK_VALKEY_*` and `AUDIENCE_FALLBACK_VALKEY_*` are a temporary
overlay used only during a resharding. Steady-state deployments leave them
unset; the wrapper unwraps itself and reads go through a single Store as
usual.

At startup the identity-agent logs one of these lines when the wrapper is
constructed:

```
audience valkey fallback enabled — reads will OR across two topologies until AUDIENCE_FALLBACK_VALKEY_* is removed
fcap valkey fallback enabled — reads will OR across two topologies until FCAP_FALLBACK_VALKEY_* is removed
```

If neither line appears, the wrapper is inert — verify with `grep 'fallback
enabled' identity-agent.log` returning empty.

The fallback is:
- **A read-side overlay only.** Writes always target the primary — the
  identity-agent doesn't write in the request path, and the separate
  frequency-writer / audience-writer services connect to the primary
  cluster directly with a cluster-aware client that follows MOVED/ASK.
- **Bounded in time.** It's meant to live for the duration of one reshard
  event (a few hours end to end). Leaving it on indefinitely doubles per-
  request shadow round-trips.
- **Not a failover mechanism.** Both sides must be up; a shadow that goes
  down during the migration is a real page.

## Full env-var reference

Both backends share the same shape. Below listed for fcap; the audience
prefix is `AUDIENCE_FALLBACK_VALKEY_*` and semantics are identical.

| Env var | Default | Notes |
|---|---|---|
| `FCAP_FALLBACK_VALKEY_MODE` | `standalone` | `standalone`, `cluster`, or `shadow`. In practice for shadow-mode deployments this is `shadow`. |
| `FCAP_FALLBACK_VALKEY_SHARDS` | *(unset — disables fallback)* | JSON `{ordinal: "host:port"}`. Presence of this env var is what turns the fallback on. |
| `FCAP_FALLBACK_VALKEY_USERNAME` | `""` | Optional Valkey ACL user. |
| `FCAP_FALLBACK_VALKEY_PASSWORD` | `""` | AUTH password. |
| `FCAP_FALLBACK_VALKEY_DB` | `0` | Logical DB (standalone only). |
| `FCAP_FALLBACK_VALKEY_TLS` | `false` | TLS to the shadow. |
| `FCAP_FALLBACK_VALKEY_DIAL_TIMEOUT` | `5s` | |
| `FCAP_FALLBACK_VALKEY_READ_TIMEOUT` | *(unset)* | Match the primary's setting. |
| `FCAP_FALLBACK_VALKEY_POOL_SIZE` | `0` (auto) | Sized like the primary. |

`Config.Validate()` rejects a fallback without a matching primary
(`FCAP_FALLBACK_VALKEY_SHARDS is set but FCAP_VALKEY_SHARDS is not`), so
you can't accidentally deploy only-a-fallback and expect it to work.

## Metrics surfaced during a fallback window

- `identity_agent_stage_duration_seconds{stage="fcap"|"audience"}` — the
  stage wraps the union read, so the histogram includes both the primary
  and fallback round-trip. Expect p50/p99 to rise by roughly one
  in-region shadow RTT while the fallback is enabled.
- `identity_agent_store_errors_total{stage="fcap"|"audience"}` — the union
  wrapper increments this when one side of the OR errored and the other
  side answered. During steady-state (fallback off) this stays at
  baseline; during a fallback window it may spike briefly around the
  primary-side reshard (union masks the error from the request path but
  records it here).
- Regular request-outcome counters (`identity_agent_stage_outcome_total`)
  are unchanged.

**Signal that the fallback is safe to remove**:
`increase(identity_agent_store_errors_total{stage="audience"}[1h])`
back at pre-migration baseline for ≥24h after the reshard completes.

## Migration runbook (7 steps)

The example below grows the audience backend from 1 primary shard to 2.
The same shape works for any N → N+1 (or, in reverse, N+1 → N — see
**Shrinking** below).

### Step 1 — Provision the new primary and shadow instances

For each region that has a primary, deploy the new primary Valkey pod
with `cluster.enabled: true` and let it come up empty (0 slots assigned).
For each region that has a shadow tier, deploy the new shadow pod with
`replicaof <primary-hostname> 6379`.

Verify:
```
valkey-cli -h <new-primary> cluster info
# cluster_state:ok
# cluster_slots_assigned:0
```

```
valkey-cli -h <new-shadow> info replication
# role:slave
# master_link_status:up
```

No traffic hits the new instances yet. Data is empty everywhere.

### Step 2 — Wait for shadow replication to be established

Even with an empty primary, verify the shadow's `slave_repl_offset`
tracks its primary and connectivity is healthy. Any replication issue
here surfaces before it matters for real data.

### Step 3 — Roll out the reader with `*_FALLBACK_VALKEY_SHARDS` set

Configure identity-agent with:
- Primary `AUDIENCE_VALKEY_SHARDS` = the **new** N+1 topology (all shards
  present, including the new one).
- Fallback `AUDIENCE_FALLBACK_VALKEY_SHARDS` = the **old** N topology
  (the one that was live pre-migration).

Restart the agent. The startup log MUST show:
```
audience valkey fallback enabled — reads will OR across two topologies until AUDIENCE_FALLBACK_VALKEY_* is removed
```

If it doesn't, the env var didn't reach the process — check ConfigMap/
Secret wiring.

At this point:
- All existing keys still physically live on the old primary/shadow.
- Reads go to both shadows in parallel; the fallback provides the answer
  for every request.
- Latency rises by ~one in-region shadow RTT. This is expected; monitor.

**Soak for ≥30 min.** This is your only chance to validate the union
path before you actually rely on it to mask a reshard. Confirm:
- No new `identity_agent_stage_outcome_total{outcome="error"}` beyond
  pre-rollout baseline.
- No new `identity_agent_store_errors_total` beyond baseline.

### Step 4 — Add the new primary to the cluster

Join the new primary to the primary cluster:
```
valkey-cli -h <old-primary> cluster meet <new-primary-ip> 6379
```

Verify from either node:
```
valkey-cli cluster nodes
# Two entries: old-primary (myself, master, slots 0-16383)
#              new-primary (master, slots -- none yet)
```

### Step 5 — Reshard slots to the new primary

The reshard MUST follow the **positional slot split** the reader assumes.
For N=2, that's:

- shard-0 keeps slots `0 .. 8191` (8192 slots)
- shard-1 gets slots `8192 .. 16383` (8192 slots)

The exact per-N boundaries are what `clusterslot.NewShardMap(N).LastSlots()`
returns. Confirm before you reshard:

```go
package main
import (
    "fmt"
    "github.com/adcontextprotocol/adcp-go/targeting/internal/clusterslot"
)
func main() { fmt.Println(clusterslot.NewShardMap(2).LastSlots()) }
// prints: [8191 16383]
```

Then reshard:
```
valkey-cli --cluster reshard <old-primary>:6379 \
  --cluster-from <old-primary-node-id> \
  --cluster-to   <new-primary-node-id> \
  --cluster-slots 8192 \
  --cluster-yes
```

Progress prints slot-by-slot. During the migration, individual keys are
`MIGRATE`d from source to destination; each key exists on exactly one
side at any instant. Union-read on the shadow tier keeps request-path
answers correct throughout because:
- Slot ownership at the primary changes as slots move.
- Each shadow replicates its primary — old-primary shadow drains keys
  that moved out (via the DEL propagated by replication), new-primary
  shadow gains keys that moved in.
- The reader's union of "old shadow view" and "new shadow view" is the
  correct answer at every instant.

### Step 6 — Wait for shadow replication to converge

After the reshard finishes, replication has a tail — the DEL/INSERT
stream from each primary to its shadow can lag by seconds. Confirm both
sides are caught up before removing the fallback:

```
kubectl exec <shadow-pod> -- valkey-cli info replication
# slave_repl_offset should equal master_repl_offset (or within a small
# constant); master_link_status:up
```

Do this for every shadow in every read region.

### Step 7 — Remove the fallback

After a ≥24h soak with `identity_agent_store_errors_total` back at
baseline, redeploy the reader with `AUDIENCE_FALLBACK_VALKEY_SHARDS`
unset (or empty). The startup log should NO LONGER contain the "fallback
enabled" line. Reader is back to single-store reads against the new
topology.

Optional cleanup:
- If the reshard is permanent, remove the old-primary-only chart values
  files (they're now the shard-0 files of the N+1 topology, so keep
  those; delete only anything that was truly retired).
- Update the identity-match runbook with the new steady-state topology.

## Rollback matrix

| At step | Action | Data-loss risk |
|---|---|---|
| 1–2 | Delete the new pods; retract terraform IPs. | None. |
| 3 | Redeploy reader with fallback env unset; wrapper unwraps. Old topology only. | None. New instances stay idle. |
| 4 | Cluster `meet` is reversible: `cluster forget <new-primary-id>` from every remaining master. No slots have moved yet. | None. |
| 5 mid-reshard | Rerun `valkey-cli --cluster reshard` in the reverse direction to move slots back. Fallback masks the intermediate state throughout. | None. |
| 5 completed but before step 7 | Same as mid-reshard: rerun in reverse. All keys land back on shard-0. Fallback still active on readers. | None. |
| 7 | Redeploy reader with fallback re-enabled if you spotted issues after removal. | Depends: if reader was running fallback-free for hours before rollback and writes accumulated on the new topology, you must reverse-reshard before the fallback is meaningful again. |

## Shrinking (N+1 → N)

Same shape in reverse:
1. Roll reader with `*_VALKEY_SHARDS` = the target N topology and
   `*_FALLBACK_VALKEY_SHARDS` = the current N+1 topology.
2. Reshard slots from the shard-to-remove back onto the surviving shards
   (positional distribution again: `clusterslot.NewShardMap(N).LastSlots()`
   defines the target boundaries).
3. After replication converges, remove the fallback.
4. Delete the retired primary + shadow pods and retract terraform IPs.

The union semantics are symmetric — a key existing on either the N or
N+1 topology answers correctly at every point.

## What NOT to do

- **Do not use a non-positional reshard.** `valkey-cli --cluster reshard`
  lets operators specify custom slot counts and non-contiguous slot
  sets. That will produce a slot-to-shard mapping the reader can't
  reproduce with `NewShardMap(N)`, and reads will end up at the wrong
  shadow. Always match the positional distribution.
- **Do not run the reader with fallback enabled long-term.** Every read
  becomes two shadow round-trips. Remove the fallback as soon as the
  primary side has been steady for the soak window.
- **Do not skip step 3's ≥30-min soak.** The soak is your only chance to
  validate the fallback is wired correctly *before* you rely on it to
  mask a reshard. Skipping this converts a routine migration into a
  full-throughput data-loss risk.
- **Do not remove the fallback before verifying step 6 convergence.**
  The DEL/INSERT stream from primary → shadow can lag; removing the
  fallback while there are still stale keys on the old shadow would
  surface as fail-open reads.
