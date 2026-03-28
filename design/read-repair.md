---
name: Read Repair
description: Post-read consistency repair that pushes merged siblings to stale replicas
type: project
---

# Read Repair

## Overview

After a quorum read, `ReadRepairer` (`internal/cluster/read_repair.go`) compares the merged canonical value against each replica's individual response. Replicas that are missing newer versions receive the merged value via a background write.

## Struct

```
nodeID   string
rpc      RPCClient
config   ReadRepairConfig
metrics  *ReadRepairMetrics
```

## Configuration (`internal/config/repair.go`)

```go
type ReadRepairConfig struct {
    Enabled     bool          // default true
    Async        bool          // default true (non-blocking)
    Timeout      time.Duration // default 1s
    Probability  float64       // default 1.0 (always trigger)
}
```

`Probability` enables probabilistic repair: a `rand.Float64()` call skips repair if the result exceeds `Probability`. Set to < 1.0 to reduce repair overhead on high-traffic keys.

## Trigger Point

`Coordinator.Get` calls `readRepairer.TriggerRepair(ctx, key, merged, responses)` after merging all sibling sets:

```
If Async == true:
    go func() { repair in new goroutine with RepairTimeout context }
Else:
    repair synchronously
```

## Staleness Check (`checkNeedsRepair`)

```go
func checkNeedsRepair(merged SiblingSet, replicaSiblings SiblingSet) bool
```

- If `replicaSiblings` is `nil` (node returned no data): always repair.
- For each sibling `s` in `merged`:
  - Check `isDominatedOrEqual(s.VClock, replicaSiblings)`.
  - If `s.VClock` is NOT dominated by any sibling in `replicaSiblings`: replica is missing `s` → repair needed.

`isDominatedOrEqual(vClock, siblings)`:
- Returns `true` if any sibling in `siblings` has a vector clock that `Dominates(vClock)` (i.e., `HappensAfter` or `Equal`).
- Returns `false` if `vClock` is newer (concurrent or ahead) — the replica is stale.

## Repair Delivery (`sendRepair`)

```
For each nodeID in read responses:
  Skip if nodeID == localNodeID (no self-repair)
  Skip if replica is up-to-date (checkNeedsRepair == false)
  rpc.RemotePut(ctx with Timeout, nodeID, key, merged)
    SUCCESS → metrics.ReadRepairSent++, ReadRepairKeysRepaired++
    FAILURE → metrics.ReadRepairFailed++
```

## Example: Stale Replica Detection

```
merged = [{vclock: {A:3, B:2}, value: "v3"}]
replicaX = [{vclock: {A:2, B:1}, value: "v2"}]

isDominatedOrEqual({A:3,B:2}, replicaX):
  replicaX[0].vclock = {A:2,B:1}
  Does {A:2,B:1}.Dominates({A:3,B:2})?  → No (A:2 < A:3)
  → replicaX needs repair → send merged to replicaX
```

## Metrics (`internal/cluster/read_repair_metrics.go`)

| Metric | Type | Description |
|---|---|---|
| `ReadRepairTriggered` | Counter | TriggerRepair calls |
| `ReadRepairSent` | Counter | Repair writes sent to replicas |
| `ReadRepairFailed` | Counter | Failed repair writes |
| `ReadRepairKeysRepaired` | Counter | Keys repaired (may be < Sent if batch) |
| `ReadRepairLatency` | Histogram (exp 0.1ms–2^14s) | Per-repair RPC duration |

## Interaction with Anti-Entropy

Read repair is **reactive** (triggered on reads) and low-latency. Anti-entropy is **proactive** (background scan). They are complementary:
- Hot keys get repaired quickly via read repair.
- Cold keys (never read) are synchronized by anti-entropy.

Changes:

