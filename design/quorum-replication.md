---
name: Quorum Replication
description: Coordinator orchestrating N/R/W quorum reads/writes with sloppy quorum, per-key locking, and sibling merging
type: project
---

# Quorum Replication

## Overview

`Coordinator` (`internal/cluster/coordinator.go`) is the central write/read orchestrator. It uses configurable N/R/W quorum parameters to fan-out operations across replica nodes, then evaluates whether enough replicas responded successfully.

## Quorum Parameters (`internal/config/quorum.go`)

| Parameter | Meaning | Default (config.yaml) |
|---|---|---|
| N | Replication factor (copies of each key) | 1 (dev mode; 3 for prod) |
| R | Read quorum (minimum replicas for valid read) | 1 |
| W | Write quorum (minimum replicas for valid write) | 1 |

Consistency guarantee: `R + W > N` implies read-write overlap → strong consistency. `R + W ≤ N` → eventual consistency. `QuorumConfig.ConsistencyLevel()` returns `"strong"` or `"eventual"`.

Preset builders: `StrongConsistencyConfig()` (N=3, R=2, W=2), `EventualConsistencyConfig()` (N=3, R=1, W=1), `HighAvailabilityConfig()` (N=5, R=2, W=2).

## Coordinator Struct (`coordinator.go`)

```
nodeID        string
ring          *HashRing
storage       Storage
rpc           RPCClient
membership    *MembershipManager
readRepairer  *ReadRepairer
antiEntropy   *AntiEntropy
hintedHandoff *HintedHandoff
keyLocks      sync.Map          — per-key mutexes
inFlight      atomic.Int64      — count of in-progress requests
metrics       CoordinatorMetrics
```

### Timeout Constants

```go
ClientTimeout  = 5s   // outer context from client
ReplicaTimeout = 2s   // per-replica RPC call
RepairTimeout  = 1s   // async read repair window
```

## Write Path (`Coordinator.Put`)

```
Put(ctx, key, value, vclock, options)
  │
  ├─ Validate key (≤64 KB) and value (≤1 MB)
  ├─ ring.GetExtendedPreferenceList(key, N, extra) → preferenceList
  ├─ vclock.Tick(localNodeID)  → advance local counter
  │
  ├─ parallelWrite(preferenceList, sibling):
  │     For each node in list:
  │       if self → localWrite(key, sibling)
  │       else   → rpc.RemotePut(ctx with ReplicaTimeout, nodeID, key, sibling)
  │
  ├─ Evaluate quorum: successCount >= W
  │     YES → return merged vclock
  │     NO  → try sloppy quorum (overflow nodes)
  │
  └─ For each failed replica → hintedHandoff.StoreHint(nodeID, key, sibling)
```

`localWrite(key, sibling)` (`coordinator.go:localWrite`):
1. Acquires `getKeyLock(key)` (a `sync.Mutex` from `keyLocks sync.Map`) — serializes concurrent writes to the same key on this node.
2. Calls `storage.Put(key, SiblingSet{sibling})`.
3. Calls `antiEntropy.OnKeyUpdate(key, siblings)` to update Merkle tree incrementally.
4. Releases lock.

## Sloppy Quorum (`coordinator.go`)

When `W` replicas cannot be reached from the standard preference list, the coordinator extends the list with overflow nodes via `GetExtendedPreferenceList(key, N, extraCount)`. Writes to overflow nodes store a **hint** pointing to the intended target. This allows writes to succeed during temporary failures, trading consistency for availability.

Only enabled when `QuorumConfig.SloppyQuorum = true`.

## Read Path (`Coordinator.Get`)

```
Get(ctx, key, options)
  │
  ├─ ring.GetPreferenceList(key, N)
  ├─ parallelRead(nodes, key):
  │     For each node:
  │       if self → storage.Get(key)
  │       else   → rpc.RemoteGet(ctx with ReplicaTimeout, nodeID, key)
  │
  ├─ Collect responses; early-return after R successes
  │
  ├─ mergeAllSiblings(responses) → canonical SiblingSet
  │     For each sibling in all responses:
  │       vclock.Merge(sibling.VClock)
  │       Keep if no other sibling HappensAfter it
  │
  ├─ filterTombstones(merged) → client-visible siblings
  │
  └─ readRepairer.TriggerRepair(ctx, key, merged, responses)
         (async or sync based on config)
```

**Early return optimization**: `parallelRead` returns as soon as R responses arrive. Remaining goroutines are cancelled via context.

## Delete Path (`Coordinator.Delete`)

Calls `Put` with a tombstone `Sibling` (flag `Tombstone=true`, value empty, clock ticked). The tombstone propagates through normal W-quorum write. Storage layer keeps tombstones until TTL GC removes them.

## Batch Operations

`BatchGet(ctx, keys []string) []BatchGetResult`: runs `Get` for each key concurrently (goroutines), collects results.

`BatchPut(ctx, items []BatchPutItem) []BatchPutResult`: runs `Put` for each item concurrently, collects results.

No cross-key atomicity — each key is independently quorum-committed.

## Per-Key Locking

`getKeyLock(key string) *sync.Mutex` — retrieves or creates a `sync.Mutex` from `keyLocks sync.Map`. Ensures that concurrent local writes to the same key are serialized, preventing lost updates during the read-modify-write in `storage.reconcileSiblings`.

## Metrics (`internal/cluster/coordinator_metrics.go`)

| Metric | Type | Description |
|---|---|---|
| `ReadLatency` | Histogram (exp 1ms–16s) | Per-read duration |
| `WriteLatency` | Histogram (exp 1ms–16s) | Per-write duration |
| `ReadSuccess` / `WriteSuccess` | Counter | Successful quorum ops |
| `ReadQuorumFailures` / `WriteQuorumFailures` | Counter | Quorum not reached |
| `GetRequestsTotal` / `PutRequestsTotal` / `DeleteRequestsTotal` | Counter | Ops by type |
| `ReplicaTimeouts` / `ReplicaErrors` | Counter | Per-replica failures |
| `HintedHandoffWrites` / `HintsReplayed` | Counter | Handoff activity |
| `ReadSiblingsCount` | Histogram (linear 1–10) | Siblings per read response |

## Error Handling

On quorum failure, returns `common.QuorumError{Required: W/R, Achieved: n, Operation: "read"/"write", ReplicaErrors: [...]}`. The server layer maps this to gRPC status `Unavailable`.

Changes:

