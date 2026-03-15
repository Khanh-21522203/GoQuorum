# Feature: Quorum-Based Replication & Coordination

## 1. Purpose

The Coordinator is the central request-handling brain of GoQuorum. It receives client Get, Put, and Delete operations and fans them out to N replica nodes according to the preference list derived from consistent hashing. It enforces configurable read (R) and write (W) quorums for tunable consistency, returns results once quorum is satisfied, and propagates errors when quorum cannot be achieved.

## 2. Responsibilities

- **Put coordination**: Generate a new vector clock, fan out the write to N replicas in parallel, return success once W acknowledgments arrive
- **Get coordination**: Fan out reads to N replicas in parallel, collect R responses, reconcile sibling sets using vector clocks, trigger read repair on stale replicas
- **Delete coordination**: Validate that the client provides a causal context (vector clock), create a tombstone sibling, fan out to N replicas with quorum W
- **Preference list resolution**: Use the hash ring to determine the ordered list of N responsible nodes for any key
- **Quorum accounting**: Track per-replica success/failure, return `QuorumError` with per-replica diagnostics when quorum is not met
- **Per-key write serialization**: Hold a per-key mutex during Put/Delete to prevent concurrent writes from racing on vector clock generation
- **Context propagation**: Carry Go `context.Context` with deadlines through all parallel RPC calls

## 3. Non-Responsibilities

- Does not implement the hash ring or virtual nodes (delegated to `hashring`)
- Does not implement the actual RPC transport to replicas (delegated to `rpc_client`)
- Does not implement storage on the local node (delegated to `storage.Engine`)
- Does not implement read repair logic beyond triggering it (delegated to `read_repair`)
- Does not implement sibling merging logic (delegated to `vclock` comparison and sibling set)
- Does not implement failure detection (delegated to `failure_detector`)

## 4. Architecture Design

```
Client gRPC Request
        |
        v
+-------------------+
|    server layer   |  (gRPC handler)
+-------------------+
        |
        v
+-------------------+
|   Coordinator     |
|                   |
|  Put(key, val):   |
|    tick vclock    |
|    get pref list  |
|    fanout N rpcs  |  ----> [Node A] Replicate(key, siblings)
|    wait W acks    |  ----> [Node B] Replicate(key, siblings)
|    return ctx     |  ----> [Node C] Replicate(key, siblings)
|                   |
|  Get(key):        |
|    get pref list  |
|    fanout N rpcs  |  ----> [Node A] Read(key)
|    wait R acks    |  ----> [Node B] Read(key)
|    reconcile sibs |  ----> [Node C] Read(key)
|    read repair    |
|    return sibs    |
+-------------------+
        |
   local node reads/writes also go through storage.Engine
```

### Quorum Profiles

| Profile  | W    | R    | Guarantee                        |
|----------|------|------|----------------------------------|
| Strong   | N    | N    | Full consistency, low availability |
| Balanced | N/2+1| N/2+1| Majority quorum, typical default |
| Weak     | 1    | 1    | Highest availability, eventual   |

## 5. Core Data Structures (Go)

```go
package cluster

import (
    "context"
    "sync"
    "time"
    "goquorum/internal/common"
    "goquorum/internal/storage"
    "goquorum/internal/vclock"
)

// Coordinator orchestrates Put/Get/Delete across N replicas.
type Coordinator struct {
    nodeID    string
    config    CoordinatorConfig
    ring      *HashRing
    local     storage.Engine
    rpcPool   *RPCClientPool
    repair    *ReadRepair
    keyLocks  *KeyLockMap

    logger  *slog.Logger
    metrics *CoordinatorMetrics
}

// CoordinatorConfig holds the N/R/W quorum parameters and timeouts.
type CoordinatorConfig struct {
    N              int           // replication factor
    R              int           // read quorum
    W              int           // write quorum
    ReplicateTimeout time.Duration
    ReadTimeout      time.Duration
}

// replicaResult is the outcome from a single replica for fanout.
type replicaResult struct {
    nodeID  string
    sibs    *storage.SiblingSet // non-nil for reads
    vclock  *vclock.VClock      // returned context for writes
    err     error
}

// QuorumError is returned when fewer than R or W replicas respond successfully.
type QuorumError struct {
    Required  int
    Succeeded int
    Errors    []ReplicaError
}

// ReplicaError pairs a node address with the error it returned.
type ReplicaError struct {
    NodeID string
    Err    error
}

// KeyLockMap provides per-key mutual exclusion for write serialization.
type KeyLockMap struct {
    mu    sync.Mutex
    locks map[string]*keyLock
}

type keyLock struct {
    mu      sync.Mutex
    waiters int
}
```

## 6. Public Interfaces

```go
package cluster

// NewCoordinator constructs a Coordinator with all required dependencies.
func NewCoordinator(
    nodeID string,
    cfg CoordinatorConfig,
    ring *HashRing,
    local storage.Engine,
    rpcPool *RPCClientPool,
    repair *ReadRepair,
    logger *slog.Logger,
) *Coordinator

// Put stores key=value with a new causal vector clock context.
// Returns the updated VClock context for the client to use in future operations.
func (c *Coordinator) Put(ctx context.Context, key, value []byte) (*vclock.VClock, error)

// Get retrieves all causally maximal siblings for key.
// Returns a reconciled SiblingSet and the merged VClock context.
func (c *Coordinator) Get(ctx context.Context, key []byte) (*storage.SiblingSet, *vclock.VClock, error)

// Delete marks key as deleted using the causal context from a prior Get.
// ctx must contain the VClock returned by a previous Get to ensure causality.
func (c *Coordinator) Delete(ctx context.Context, key []byte, causalCtx *vclock.VClock) error

// (QuorumError).Error() returns a human-readable error string.
func (e *QuorumError) Error() string
```

## 7. Internal Algorithms

### Put Algorithm
```
Put(ctx, key, value):
  validate key/value size constraints
  keyLocks.Lock(key)
  defer keyLocks.Unlock(key)

  vc = local.GetVClock(key) or vclock.New()
  vc.Tick(nodeID)

  newSib = storage.NewSibling(value, vc, timestamp=now())
  newSibSet = storage.SiblingSet{newSib}

  prefList = ring.PreferenceList(key, N)
  results = make(chan replicaResult, N)

  for each node in prefList:
    go fanoutReplicate(ctx, node, key, newSibSet, results)

  successes = 0
  errors = []
  for i in 0..N:
    r = <-results
    if r.err == nil:
      successes++
      if successes >= W:
        return newSib.VClock, nil  // early return at quorum
    else:
      errors = append(errors, ReplicaError{r.nodeID, r.err})

  return nil, QuorumError{W, successes, errors}
```

### Get Algorithm
```
Get(ctx, key):
  prefList = ring.PreferenceList(key, N)
  results = make(chan replicaResult, N)

  for each node in prefList:
    go fanoutRead(ctx, node, key, results)

  collected = []SiblingSet{}
  successes = 0
  errors = []
  for i in 0..N:
    r = <-results
    if r.err == nil:
      collected = append(collected, r.sibs)
      successes++
      if successes >= R:
        break  // early return at quorum
    else:
      errors = append(errors, ReplicaError{r.nodeID, r.err})

  if successes < R:
    return nil, nil, QuorumError{R, successes, errors}

  // Drain remaining results for read repair
  go drainAndRepair(results, collected, prefList, key)

  // Reconcile: merge all SiblingSets, keep causally maximal
  merged = reconcile(collected...)
  merged = merged.FilterTombstones()
  if merged.IsEmpty():
    return nil, nil, ErrNotFound

  mergedVC = merged.MaxVClock()
  return merged, mergedVC, nil
```

### Delete Algorithm
```
Delete(ctx, key, causalCtx):
  validate causalCtx is non-nil (require prior Get)
  keyLocks.Lock(key)
  defer keyLocks.Unlock(key)

  causalCtx.Tick(nodeID)
  tombstone = storage.NewTombstoneSibling(causalCtx, timestamp=now())
  sibSet = storage.SiblingSet{tombstone}

  prefList = ring.PreferenceList(key, N)
  // fanout same as Put
  ...
  return quorum result
```

### Sibling Reconciliation
```
reconcile(sibSets...):
  all = union of all siblings across all sets
  maximal = []
  for each sib A in all:
    dominated = false
    for each sib B in all where B != A:
      if B.VClock.HappensAfter(A.VClock):
        dominated = true
        break
    if !dominated:
      maximal = append(maximal, A)
  return deduplicate(maximal)  // by VClock equality
```

### fanoutReplicate
```
fanoutReplicate(ctx, node, key, sibSet, results):
  if node == localNodeID:
    err = local.Put(key, sibSet)
    results <- replicaResult{node, nil, sibSet.MaxVClock(), err}
    return

  client = rpcPool.Get(node)
  resp, err = client.Replicate(ctx, key, sibSet)
  results <- replicaResult{node, nil, resp.VClock, err}
```

## 8. Persistence Model

The Coordinator is stateless — it delegates all persistence to `storage.Engine` on the local node and to remote nodes via RPC. The only in-memory state is the `KeyLockMap`, which is rebuilt fresh on restart and holds no durable data.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `KeyLockMap` per-key mutex | Serializes concurrent Puts/Deletes for the same key to prevent vector clock races |
| `sync.WaitGroup` / channel fan-out | N goroutines launched per request; coordinator collects results via buffered channel |
| `context.Context` with deadline | Propagated to all RPC calls; goroutines cancel early on context expiry |
| No global coordinator lock | Reads to different keys proceed fully in parallel |

Per-key locking is held only for the duration of vector clock generation + RPC fanout. Reads do not acquire key locks.

## 10. Configuration

```go
type CoordinatorConfig struct {
    N                int           `yaml:"n" default:"3"`
    R                int           `yaml:"r" default:"2"`
    W                int           `yaml:"w" default:"2"`
    ReplicateTimeout time.Duration `yaml:"replicate_timeout" default:"5s"`
    ReadTimeout      time.Duration `yaml:"read_timeout" default:"5s"`
    DeleteTimeout    time.Duration `yaml:"delete_timeout" default:"5s"`
}
```

Constraint: `R + W > N` must hold for strong consistency. Validated at startup.

## 11. Observability

- `goquorum_coordinator_put_total{result="success|quorum_error|error"}` — Put outcome counter
- `goquorum_coordinator_get_total{result="success|not_found|quorum_error|error"}` — Get outcome counter
- `goquorum_coordinator_delete_total{result="success|quorum_error|error"}` — Delete outcome counter
- `goquorum_coordinator_put_duration_seconds` — Put latency histogram
- `goquorum_coordinator_get_duration_seconds` — Get latency histogram
- `goquorum_coordinator_sibling_count` — Histogram of sibling counts returned per Get
- `goquorum_coordinator_replica_errors_total{node,operation}` — Per-replica error counter
- Log at WARN when quorum not met, including which replicas failed

## 12. Testing Strategy

- **Unit tests**:
  - `TestPutAchievesWriteQuorum`: mock N replicas, W succeed, assert success
  - `TestPutQuorumFailure`: mock N replicas, fewer than W succeed, assert QuorumError
  - `TestGetReconcilesSiblings`: mock R replicas returning different sibling sets, assert merged result is causally maximal
  - `TestGetFiltersCompletedTombstones`: assert Get returns ErrNotFound when only tombstones remain
  - `TestDeleteRequiresCausalContext`: assert Delete with nil causal context returns error
  - `TestPerKeyLockSerialization`: two concurrent Puts for same key, assert vector clocks are properly ordered (not concurrent)
  - `TestEarlyReturnAtQuorum`: assert coordinator returns before all N replicas respond once quorum achieved
- **Integration tests**:
  - `TestPutGetDelete3Nodes`: 3-node in-process cluster, verify end-to-end Put→Get→Delete cycle
  - `TestQuorumWithOneNodeDown`: bring down one replica, verify W=2 writes still succeed

## 13. Open Questions

None.
