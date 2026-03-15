# Feature: Hinted Handoff

## 1. Purpose

Hinted handoff is a write-availability mechanism borrowed directly from Amazon Dynamo. When a write targets N replicas but one or more preferred replicas are unavailable, the coordinator accepts the write on behalf of the unreachable node — storing it locally as a "hint" with metadata indicating the intended recipient. When the target node recovers, the hinting node forwards all buffered hints, ensuring that no write is permanently lost due to a transient node failure. This feature is not yet implemented.

## 2. Responsibilities

- **Hint creation**: When a preferred replica is unreachable during a Put/Delete, create a hint record containing the key, sibling set, intended node ID, and creation timestamp
- **Hint storage**: Persist hints in a dedicated Pebble column family (or key prefix) so they survive restarts
- **Hint delivery**: Background goroutine monitors node recovery (via failure detector events) and forwards pending hints to the recovered node via `Replicate` RPC
- **Hint expiry**: Discard hints older than a configurable TTL to bound disk usage from long-lived partitions
- **Per-node hint queue**: Maintain separate hint queues per target node to avoid a large backlog for one node blocking delivery to others
- **Quorum interaction**: Allow the coordinator to count a hinted write as a successful replica response for the purposes of meeting write quorum (sloppy quorum behavior — see `plan-sloppy-quorum.md`)

## 3. Non-Responsibilities

- Does not guarantee delivery if the hinting node itself fails before delivering hints (anti-entropy covers this residual case)
- Does not implement the sloppy quorum decision logic (see `plan-sloppy-quorum.md`)
- Does not implement anti-entropy (hints are a separate short-term buffer; AE is the long-term repair)
- Does not compress hint values (raw sibling set bytes are stored)

## 4. Architecture Design

```
Coordinator.Put (preferred replica N2 is unavailable):
    |
    |-- fanout to [N1: success, N2: error, N3: success]
    |
    |  W=2 quorum met with N1 + N3
    |
    +-- HintedHandoff.StoreHint(key, sibSet, targetNode=N2)
           |
           v
      Pebble: prefix "hint/<N2>/<seq>" → {key, sibSet, ts, target}

HintDelivery background goroutine:
  on N2 recovery (FailureDetector onChange event):
    hints = HintStore.HintsFor("N2")
    for each hint:
      err = rpcPool.Replicate(ctx, "N2", hint.Key, hint.SibSet)
      if err == nil:
        HintStore.Delete(hint.ID)
      else:
        break (N2 still unreachable, retry next cycle)
```

## 5. Core Data Structures (Go)

```go
package cluster

import (
    "time"
    "goquorum/internal/storage"
)

// HintedHandoff manages the storage and delivery of write hints.
type HintedHandoff struct {
    config   HintedHandoffConfig
    store    HintStore
    rpcPool  *RPCClientPool
    detector *FailureDetector
    stopCh   chan struct{}
    wg       sync.WaitGroup
    logger   *slog.Logger
    metrics  *HintMetrics
}

// Hint represents one buffered write destined for a temporarily unavailable node.
type Hint struct {
    ID         string    // unique hint ID (UUID)
    TargetNode string    // node ID that should have received this write
    Key        []byte
    SibSet     *storage.SiblingSet
    CreatedAt  time.Time
}

// HintStore is the persistence interface for hints.
type HintStore interface {
    // StoreHint persists a hint for the target node.
    StoreHint(h *Hint) error
    // HintsFor returns all hints destined for targetNode, ordered by creation time.
    HintsFor(targetNode string) ([]*Hint, error)
    // DeleteHint removes a delivered hint by ID.
    DeleteHint(id string) error
    // PruneExpired removes hints older than maxAge.
    PruneExpired(maxAge time.Duration) (int, error)
    // HintCount returns the number of pending hints per target node.
    HintCount() (map[string]int, error)
}

// HintedHandoffConfig holds tuning parameters.
type HintedHandoffConfig struct {
    Enabled         bool          `yaml:"enabled" default:"true"`
    MaxHintsPerNode int           `yaml:"max_hints_per_node" default:"10000"`
    HintTTL         time.Duration `yaml:"hint_ttl" default:"24h"`
    DeliveryInterval time.Duration `yaml:"delivery_interval" default:"30s"`
    DeliveryTimeout  time.Duration `yaml:"delivery_timeout" default:"10s"`
    MaxDeliveryBatch int          `yaml:"max_delivery_batch" default:"500"`
}
```

## 6. Public Interfaces

```go
package cluster

// NewHintedHandoff creates a HintedHandoff instance.
func NewHintedHandoff(
    cfg HintedHandoffConfig,
    store HintStore,
    rpcPool *RPCClientPool,
    detector *FailureDetector,
    logger *slog.Logger,
) *HintedHandoff

// StoreHint persists a hint for the target node.
// Called by the Coordinator when a preferred replica is unavailable.
// Returns error only for storage failures; hint loss is acceptable under disk pressure.
func (hh *HintedHandoff) StoreHint(key []byte, sibSet *storage.SiblingSet, targetNode string) error

// Start launches the background hint delivery goroutine.
// Subscribes to FailureDetector state change events.
func (hh *HintedHandoff) Start()

// Stop drains in-flight deliveries and shuts down the background goroutine.
func (hh *HintedHandoff) Stop()

// NewPebbleHintStore creates a hint store backed by a Pebble key prefix.
// Uses the same Pebble DB as the main storage engine with a "hint/" key prefix.
func NewPebbleHintStore(db *pebble.DB) *PebbleHintStore
```

## 7. Internal Algorithms

### StoreHint
```
StoreHint(key, sibSet, targetNode):
  count = store.HintCount()[targetNode]
  if count >= config.MaxHintsPerNode:
    metrics.HintsDropped.WithLabelValues(targetNode).Inc()
    log.Warn("hint dropped: queue full", target=targetNode)
    return nil  // degrade gracefully — don't fail the write

  hint = Hint{
    ID:         newUUID(),
    TargetNode: targetNode,
    Key:        key,
    SibSet:     sibSet,
    CreatedAt:  now(),
  }
  err = store.StoreHint(hint)
  if err == nil:
    metrics.HintsStored.WithLabelValues(targetNode).Inc()
  return err
```

### Delivery Loop
```
deliveryLoop():
  ticker = time.NewTicker(config.DeliveryInterval)
  for:
    select:
    case <-ticker.C:
      go deliverAllPending()
    case <-hh.stopCh:
      return

deliverAllPending():
  // Prune expired hints first
  store.PruneExpired(config.HintTTL)

  // Try to deliver to each node that has hints
  counts, _ = store.HintCount()
  for targetNode in keys(counts):
    state = detector.PeerState(targetNode)
    if state != NodeStateActive:
      continue  // not recovered yet

    go deliverHintsTo(targetNode)
```

### deliverHintsTo
```
deliverHintsTo(targetNode):
  hints, err = store.HintsFor(targetNode)
  if err != nil || len(hints) == 0: return

  // Deliver in batches
  for i = 0; i < len(hints); i += config.MaxDeliveryBatch:
    batch = hints[i : min(i+config.MaxDeliveryBatch, len(hints))]
    for _, hint in batch:
      ctx, cancel = context.WithTimeout(background, config.DeliveryTimeout)
      err = rpcPool.Replicate(ctx, targetNode, hint.Key, hint.SibSet)
      cancel()
      if err != nil:
        metrics.DeliveryError.WithLabelValues(targetNode).Inc()
        return  // stop delivery if node went down again
      store.DeleteHint(hint.ID)
      metrics.DeliverySuccess.WithLabelValues(targetNode).Inc()
```

### PebbleHintStore Key Format
```
hint/<targetNodeID>/<sequenceNumber> → encoded Hint
```
Keys are lexicographically ordered, so iterating with prefix `hint/<nodeID>/` yields hints in creation order.

## 8. Persistence Model

Hints are stored in the main Pebble database under the `hint/` key prefix. This co-location avoids managing a separate database file. Hints are encoded as protobuf or binary-encoded structs (same encoding as `SiblingSet` but with hint metadata prepended).

Hint records are individually deleted upon successful delivery. Expired hints are bulk-deleted by `PruneExpired` at the start of each delivery cycle.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `deliverAllPending` goroutines | One goroutine per target node; parallel delivery to multiple recovered nodes |
| Per-node delivery serialization | Only one delivery goroutine per target node at a time (guard with `sync.Map` of in-flight deliveries) |
| Pebble iterator for scan | Safe concurrent reads alongside application writes |
| `sync.WaitGroup` in Stop | Waits for all in-flight deliveries to complete before shutdown |

## 10. Configuration

```go
type HintedHandoffConfig struct {
    Enabled          bool          `yaml:"enabled" default:"true"`
    MaxHintsPerNode  int           `yaml:"max_hints_per_node" default:"10000"`
    HintTTL          time.Duration `yaml:"hint_ttl" default:"24h"`
    DeliveryInterval time.Duration `yaml:"delivery_interval" default:"30s"`
    DeliveryTimeout  time.Duration `yaml:"delivery_timeout" default:"10s"`
    MaxDeliveryBatch int           `yaml:"max_delivery_batch" default:"500"`
}
```

## 11. Observability

- `goquorum_hints_stored_total{target}` — Counter of hints created per target node
- `goquorum_hints_delivered_total{target}` — Counter of successfully delivered hints
- `goquorum_hints_delivery_error_total{target}` — Counter of failed delivery attempts
- `goquorum_hints_dropped_total{target}` — Counter of hints dropped due to queue full
- `goquorum_hints_expired_total{target}` — Counter of hints pruned by TTL
- `goquorum_hints_pending{target}` — Gauge of current pending hints per target
- Log at INFO when a node recovers and hint delivery begins
- Log at INFO when all hints for a recovered node are delivered
- Log at WARN when hints are dropped due to max queue size
- Log at WARN when hints expire before delivery (node was down longer than TTL)

## 12. Testing Strategy

- **Unit tests**:
  - `TestStoreHintPersists`: store a hint, retrieve via HintsFor, assert fields match
  - `TestStoreHintDropsWhenFull`: store MaxHintsPerNode hints, store one more, assert drop counter incremented
  - `TestDeliverySucceeds`: store hint, mark target as ACTIVE, run delivery cycle, assert Replicate called and hint deleted
  - `TestDeliverySkipsInactiveNodes`: store hint for FAILED node, run delivery, assert Replicate NOT called
  - `TestDeliveryStopsOnError`: Replicate fails mid-batch, assert remaining hints not attempted
  - `TestExpiredHintsPruned`: store hint with CreatedAt=now-25h (TTL=24h), run delivery cycle, assert hint deleted
  - `TestDeliveryBatchSizeRespected`: 1200 hints, MaxDeliveryBatch=500, assert 3 batches
  - `TestPebbleHintStoreKeyPrefix`: hint stored under `hint/` prefix, verify no collision with user keys
- **Integration tests**:
  - `TestHintedHandoffEndToEnd`: 3 nodes, bring down N2, write key, bring N2 back, assert N2 eventually has the key
  - `TestHintDeliveryAfterRestart`: store hints, restart hinting node, verify hints delivered after restart

## 13. Open Questions

- Should hinted writes count toward write quorum (sloppy quorum) by default, or require explicit configuration? This is covered in `plan-sloppy-quorum.md`.
- Should the hint store use a separate Pebble instance to avoid hint I/O competing with user data I/O?
