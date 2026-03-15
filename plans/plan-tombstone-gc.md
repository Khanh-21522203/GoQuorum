# Feature: Tombstone Garbage Collection

## 1. Purpose

When a key is deleted in GoQuorum, a tombstone sibling is written rather than immediately removing the key. This is necessary because the tombstone must propagate to all replicas via normal replication and anti-entropy before being safely removed. The Tombstone GC subsystem periodically scans local storage and physically deletes keys whose tombstones have outlived the configured TTL — ensuring that deleted keys are eventually reclaimed from disk while avoiding premature removal before all replicas have seen the deletion.

## 2. Responsibilities

- **Periodic scan**: Walk all keys in local Pebble storage on a configurable interval
- **Eligibility check**: A key is eligible for physical deletion if all of its siblings are tombstones AND the oldest tombstone's timestamp is older than the configured TTL
- **Physical deletion**: Remove eligible keys from Pebble in a batch write
- **Merkle tree notification**: Notify anti-entropy's Merkle tree to remove the key's contribution to its bucket hash
- **Metrics and logging**: Track deletion counts, scan duration, and error rates

## 3. Non-Responsibilities

- Does not remove tombstones that have not yet propagated (that is why TTL must be conservatively large)
- Does not coordinate tombstone GC across nodes — each node runs GC independently
- Does not implement the storage scan (uses `storage.Engine.Scan`)
- Does not choose tombstone TTL based on cluster state — TTL is a static configuration value

## 4. Architecture Design

```
TombstoneGC background goroutine:
  every gc_interval:
    iter = engine.Scan(nil, nil)  // full keyspace scan
    batch = []keysToDelete
    for each (key, sibSet) in iter:
      if sibSet.AllTombstones() && time.Since(sibSet.OldestTombstoneTime()) > tombstone_ttl:
        batch = append(batch, key)
    engine.BatchDelete(batch)
    merkleTree.RemoveKeys(batch)
    metrics.GCDeletedKeys.Add(len(batch))
```

## 5. Core Data Structures (Go)

```go
package storage

import (
    "time"
    "sync"
)

// TombstoneGC runs periodic garbage collection of expired tombstones.
type TombstoneGC struct {
    config   TombstoneGCConfig
    engine   Engine
    tree     MerkleTreeUpdater
    stopCh   chan struct{}
    wg       sync.WaitGroup
    logger   *slog.Logger
    metrics  *GCMetrics
}

// TombstoneGCConfig holds GC tuning parameters.
type TombstoneGCConfig struct {
    // Enabled controls whether GC runs at all.
    Enabled bool `yaml:"enabled" default:"true"`
    // Interval is how often a GC scan is triggered.
    Interval time.Duration `yaml:"gc_interval" default:"1h"`
    // TombstoneTTL is how long after deletion before physical removal.
    // Must be larger than the maximum expected clock skew + anti-entropy convergence time.
    TombstoneTTL time.Duration `yaml:"tombstone_ttl" default:"168h"` // 7 days
    // BatchSize is the maximum number of keys deleted per Pebble batch.
    BatchSize int `yaml:"batch_size" default:"1000"`
}

// MerkleTreeUpdater is the interface TombstoneGC uses to notify the Merkle tree.
type MerkleTreeUpdater interface {
    RemoveKey(key []byte)
}

// GCMetrics tracks garbage collection activity.
type GCMetrics struct {
    DeletedKeysTotal prometheus.Counter
    ScanDuration     prometheus.Histogram
    ErrorTotal       prometheus.Counter
}
```

## 6. Public Interfaces

```go
package storage

// NewTombstoneGC creates a TombstoneGC instance.
func NewTombstoneGC(
    cfg TombstoneGCConfig,
    engine Engine,
    tree MerkleTreeUpdater,
    logger *slog.Logger,
    metrics *GCMetrics,
) *TombstoneGC

// Start launches the background GC goroutine.
func (gc *TombstoneGC) Start()

// Stop gracefully shuts down the GC goroutine. Blocks until the current scan (if any) completes.
func (gc *TombstoneGC) Stop()

// RunOnce executes exactly one GC scan synchronously.
// Used for testing and admin-triggered GC.
func (gc *TombstoneGC) RunOnce() (deletedCount int, err error)
```

## 7. Internal Algorithms

### GC Loop
```
gcLoop():
  ticker = time.NewTicker(gc.config.Interval)
  for:
    select:
    case <-ticker.C:
      count, err = gc.RunOnce()
      if err != nil:
        gc.metrics.ErrorTotal.Inc()
        log.Error("GC scan failed", err=err)
      else:
        log.Info("GC scan complete", deleted=count)
    case <-gc.stopCh:
      return
```

### RunOnce
```
RunOnce():
  start = now()
  iter = gc.engine.Scan(nil, nil)
  defer iter.Close()

  batch = [][]byte{}
  for iter.Next():
    key = iter.Key()
    sibSet, err = iter.Value()
    if err != nil:
      gc.metrics.ErrorTotal.Inc()
      continue

    if sibSet.AllTombstones():
      oldest = sibSet.OldestTombstoneTime()
      if time.Since(oldest) > gc.config.TombstoneTTL:
        batch = append(batch, copyBytes(key))

        // Flush batch when it reaches BatchSize
        if len(batch) >= gc.config.BatchSize:
          gc.deleteBatch(batch)
          batch = batch[:0]

  // Flush remaining
  if len(batch) > 0:
    gc.deleteBatch(batch)

  gc.metrics.ScanDuration.Observe(time.Since(start).Seconds())
  gc.metrics.DeletedKeysTotal.Add(float64(totalDeleted))
  return totalDeleted, nil
```

### deleteBatch
```
deleteBatch(keys):
  err = gc.engine.BatchDelete(keys)
  if err != nil:
    gc.metrics.ErrorTotal.Inc()
    log.Error("GC batch delete failed", keys=len(keys), err=err)
    return

  for _, key in keys:
    gc.tree.RemoveKey(key)

  totalDeleted += len(keys)
```

## 8. Persistence Model

GC physically removes keys from Pebble via `db.Delete`. Pebble's WAL ensures the deletion is durable once committed. After a GC scan, the deleted keys are gone from the LSM tree and will be reclaimed by Pebble's background compaction.

No GC state (e.g., scan cursor, last-run timestamp) is persisted. Each GC run is a full scan from the beginning of the key space. This is safe because GC only deletes keys meeting strict eligibility criteria regardless of scan order.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Single GC goroutine | Only one GC scan runs at a time; no concurrent scans |
| Engine's Pebble snapshot iterator | GC scan uses a consistent snapshot; concurrent writes are invisible to the current scan and will be picked up in the next round |
| `sync.WaitGroup` in Stop | Ensures the current scan completes before shutdown |

GC does not hold any application-level locks. It uses `engine.BatchDelete` which is Pebble-serialized. The Coordinator's key-level locks are not involved in GC because GC only deletes already-tombstoned keys that no client will be actively writing.

## 10. Configuration

```go
type TombstoneGCConfig struct {
    Enabled      bool          `yaml:"enabled" default:"true"`
    Interval     time.Duration `yaml:"gc_interval" default:"1h"`
    TombstoneTTL time.Duration `yaml:"tombstone_ttl" default:"168h"`
    BatchSize    int           `yaml:"batch_size" default:"1000"`
}
```

**TTL guidance**: The TTL must exceed the maximum time it takes for a deletion to propagate to all replicas. A node that was partitioned for 24 hours should still receive the tombstone via anti-entropy before the TTL expires. 7 days is a conservative default for most deployments. Lower TTL only if storage reclamation is urgent and cluster partitions are known to be short-lived.

## 11. Observability

- `goquorum_gc_deleted_keys_total` — Counter of physically deleted keys across all GC scans
- `goquorum_gc_scan_duration_seconds` — Histogram of full GC scan duration
- `goquorum_gc_error_total` — Counter of errors during GC (scan errors, delete failures)
- Log at INFO at the start and end of each GC scan with the deletion count
- Log at WARN if a scan takes longer than `Interval / 2` (scan duration approaching next trigger)

## 12. Testing Strategy

- **Unit tests**:
  - `TestGCDeletesExpiredTombstone`: insert tombstone with timestamp=now-8days, run GC, assert key deleted
  - `TestGCPreservesRecentTombstone`: insert tombstone with timestamp=now-1hour, run GC, assert key NOT deleted
  - `TestGCPreservesLiveKeys`: insert live (non-tombstone) sibling, run GC, assert key NOT deleted
  - `TestGCMixedSiblings`: one tombstone + one live sibling, run GC, assert key NOT deleted (not all tombstones)
  - `TestGCBatchSize`: insert 2500 expired keys, assert deleteBatch called 3 times (BatchSize=1000)
  - `TestGCMerkleTreeNotified`: expired key deleted, assert MerkleTreeUpdater.RemoveKey called
  - `TestGCRunOnceReturnsCount`: insert 5 expired tombstones, RunOnce(), assert returns 5
  - `TestGCStopMidScan`: stop GC during scan, assert goroutine exits cleanly
- **Integration tests**:
  - `TestGCReclaimsDiskSpace`: write and delete 10,000 keys, advance clock past TTL, run GC, assert disk usage decreases
  - `TestGCPreservesActiveData`: mixed live and tombstone keys, run GC, assert only tombstones past TTL deleted

## 13. Open Questions

None.
