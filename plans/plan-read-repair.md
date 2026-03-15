# Feature: Read Repair

## 1. Purpose

Read repair is a background consistency mechanism that lazily heals stale replicas during normal read operations. When a Get discovers that some replicas returned an older version of a key than others, it schedules a write of the latest version back to the stale replicas — without waiting for the repair to complete before returning to the client. This keeps replicas converging without requiring a dedicated synchronization pass for every stale read.

## 2. Responsibilities

- **Staleness detection**: After a Get collects R or more responses, compare sibling sets across all responding replicas to identify which nodes returned a causally dominated (older) version
- **Repair scheduling**: Enqueue repair work for stale replicas into a bounded channel for async processing
- **Repair execution**: Send the latest sibling set to each stale replica via `Replicate` RPC
- **Probabilistic triggering**: Apply a configurable probability (0.0–1.0) to sub-sample repair triggering, preventing thundering-herd on hot keys
- **Sync mode**: Support a synchronous repair mode (for testing) that blocks until all repairs complete before the coordinator returns
- **Backpressure**: Drop repair requests when the work queue is full rather than blocking the read path

## 3. Non-Responsibilities

- Does not detect or repair all divergence across the cluster — that is anti-entropy's job
- Does not repair keys that were not read (lazy, not eager)
- Does not initiate repairs for keys below the R quorum threshold
- Does not coordinate across multiple coordinators (each repairs independently)

## 4. Architecture Design

```
Coordinator.Get()
    |
    |-- collect R+ responses from replicas
    |-- reconcile siblings (find maximal version)
    |
    v
ReadRepair.MaybeRepair(key, maxSiblingSet, allResults)
    |
    |-- filter stale replicas (those with older sibling sets)
    |-- apply probability filter
    |
    +-- [async mode]: enqueue into repairChan
    |       |
    |       v
    |   repairWorker goroutine
    |       |
    |       +--> rpcPool.Replicate(staleNode, key, maxSiblingSet)
    |
    +-- [sync mode]: call repairs inline, wait for all
```

## 5. Core Data Structures (Go)

```go
package cluster

import (
    "context"
    "math/rand"
    "sync"
    "goquorum/internal/storage"
)

// ReadRepair detects and heals stale replicas during read operations.
type ReadRepair struct {
    config    ReadRepairConfig
    rpcPool   *RPCClientPool
    localID   string
    local     storage.Engine
    repairCh  chan repairTask    // bounded async work queue
    wg        sync.WaitGroup

    logger  *slog.Logger
    metrics *ReadRepairMetrics
}

// repairTask represents one pending repair to a single stale replica.
type repairTask struct {
    key      []byte
    nodeID   string
    sibSet   *storage.SiblingSet
}

// replicaReadResult is the outcome from one replica's Read call,
// as collected by the Coordinator fanout.
type replicaReadResult struct {
    nodeID string
    sibSet *storage.SiblingSet
    err    error
}
```

## 6. Public Interfaces

```go
package cluster

// NewReadRepair constructs a ReadRepair with a background worker pool.
func NewReadRepair(
    cfg ReadRepairConfig,
    localID string,
    local storage.Engine,
    rpcPool *RPCClientPool,
    logger *slog.Logger,
) *ReadRepair

// MaybeRepair checks all replica results for staleness against maxSibSet.
// Stale replicas are asynchronously sent the maxSibSet.
// If cfg.Sync is true, this blocks until all repairs are dispatched and ACKed.
func (r *ReadRepair) MaybeRepair(
    ctx context.Context,
    key []byte,
    maxSibSet *storage.SiblingSet,
    results []replicaReadResult,
)

// Start launches the background repair worker goroutine(s).
func (r *ReadRepair) Start()

// Stop gracefully drains the repair queue and stops workers.
func (r *ReadRepair) Stop()
```

## 7. Internal Algorithms

### MaybeRepair
```
MaybeRepair(ctx, key, maxSibSet, results):
  // Probability check — skip if random float > probability
  if rand.Float64() > r.config.Probability:
    r.metrics.SkippedTotal.Inc()
    return

  stale = []replicaReadResult{}
  for each result in results:
    if result.err != nil: continue
    // A replica is stale if its sibling set is causally dominated by maxSibSet
    if isStale(result.sibSet, maxSibSet):
      stale = append(stale, result)

  if len(stale) == 0: return

  if r.config.Sync:
    for each s in stale:
      err = r.repairOne(ctx, key, s.nodeID, maxSibSet)
      r.metrics.record(err)
    return

  // Async: enqueue, drop if full (non-blocking send)
  for each s in stale:
    task = repairTask{key, s.nodeID, maxSibSet}
    select:
    case r.repairCh <- task:
      r.metrics.EnqueuedTotal.Inc()
    default:
      r.metrics.DroppedTotal.Inc()  // backpressure: queue full
```

### isStale
```
isStale(replicaSibSet, maxSibSet):
  // A replica is stale if its merged VClock happens-before maxSibSet's VClock
  replicaVC = replicaSibSet.MaxVClock()
  maxVC = maxSibSet.MaxVClock()
  return replicaVC.HappensBefore(maxVC) || replicaVC.Compare(maxVC) == Equal && replicaSibSet.Len() < maxSibSet.Len()
```

### repairWorker (background goroutine)
```
repairWorker():
  for task := range r.repairCh:
    ctx, cancel = context.WithTimeout(background, r.config.RepairTimeout)
    err = r.repairOne(ctx, task.key, task.nodeID, task.sibSet)
    cancel()
    r.metrics.record(err)
```

### repairOne
```
repairOne(ctx, key, nodeID, sibSet):
  if nodeID == r.localID:
    return r.local.Put(key, sibSet)
  client = r.rpcPool.Get(nodeID)
  return client.Replicate(ctx, key, sibSet)
```

## 8. Persistence Model

Read repair is entirely stateless. It does not persist any pending repairs across restarts. If a node crashes before the repair channel drains, those repairs are lost and must be handled by anti-entropy on the next sync cycle.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Buffered channel `repairCh` | Decouples the read path (producer) from repair execution (consumer); sized by `QueueSize` |
| Background goroutine pool | `WorkerCount` goroutines drain the channel concurrently |
| Non-blocking channel send | `MaybeRepair` never blocks the read path — drops tasks if queue full |
| `sync.WaitGroup` | `Stop()` waits for all in-flight repairs to complete before returning |

## 10. Configuration

```go
type ReadRepairConfig struct {
    // Enabled controls whether read repair runs at all.
    Enabled bool `yaml:"enabled" default:"true"`
    // Probability is the fraction of reads that trigger repair (0.0–1.0).
    Probability float64 `yaml:"probability" default:"1.0"`
    // Async: if true (default), repairs run in background; if false, inline (for tests).
    Async bool `yaml:"async" default:"true"`
    // QueueSize is the maximum number of pending async repairs.
    QueueSize int `yaml:"queue_size" default:"1000"`
    // WorkerCount is the number of background repair goroutines.
    WorkerCount int `yaml:"worker_count" default:"4"`
    // RepairTimeout is the deadline for each individual repair RPC.
    RepairTimeout time.Duration `yaml:"repair_timeout" default:"5s"`
}
```

## 11. Observability

- `goquorum_read_repair_triggered_total` — Counter of repair sessions initiated
- `goquorum_read_repair_stale_replicas_total{node}` — Counter of stale replicas detected per node
- `goquorum_read_repair_success_total` — Counter of successful repairs
- `goquorum_read_repair_error_total{node}` — Counter of failed repair RPCs
- `goquorum_read_repair_dropped_total` — Counter of repairs dropped due to full queue
- `goquorum_read_repair_skipped_total` — Counter of skipped repairs due to probability filter
- `goquorum_read_repair_queue_depth` — Gauge of current repair channel depth
- Log at DEBUG when stale replicas detected, including key and stale node IDs

## 12. Testing Strategy

- **Unit tests**:
  - `TestMaybeRepairDetectsStaleReplica`: two replicas with different sibling sets, assert stale one is repaired
  - `TestMaybeRepairSkipsUpToDate`: all replicas have same version, assert no repairs triggered
  - `TestProbabilityFilter`: with probability=0.0, assert no repairs triggered; with 1.0, all triggered
  - `TestSyncMode`: sync=true, assert repairs complete before MaybeRepair returns
  - `TestAsyncBackpressure`: fill queue (QueueSize=1), assert additional repairs dropped without blocking
  - `TestRepairWorkerRPCError`: repair RPC fails, assert error counter incremented, no panic
  - `TestLocalNodeRepair`: stale replica is the local node, assert local storage written directly
  - `TestStopDrainsQueue`: enqueue repairs, call Stop(), assert all complete before return
- **Integration tests**:
  - `TestReadRepairConvergesCluster`: write to one node, read from different node, assert stale node eventually repaired

## 13. Open Questions

None.
