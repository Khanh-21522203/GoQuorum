# Feature: Storage Engine

## 1. Purpose

The storage engine is the durable persistence layer of GoQuorum. It wraps Pebble (CockroachDB's LSM tree engine) and provides a typed interface for reading and writing `SiblingSet` values keyed by arbitrary byte keys. It also handles tombstone garbage collection and exposes compaction controls. All other components access local key-value data only through this interface.

## 2. Responsibilities

- **Key-value CRUD**: Implement `Get`, `Put`, `Delete` (tombstone write), and `Merge` operations on `SiblingSet` values
- **Encoding/decoding**: Encode `SiblingSet` to bytes before writing and decode on read using the binary format with CRC32 checksum
- **Tombstone GC**: Periodically scan for keys whose entire sibling set consists of tombstones past the configured TTL, and physically delete them from Pebble
- **Compaction**: Expose a manual `TriggerCompaction()` for admin use, and configure Pebble's automatic compaction thresholds
- **Iteration**: Provide a `Scan` iterator over all keys for use by anti-entropy tree rebuilding and GC
- **Metrics**: Record per-operation latencies, key count, disk size, and GC stats via Prometheus
- **Health reporting**: Provide a `DiskUsage()` call for health checks and heartbeat status

## 3. Non-Responsibilities

- Does not implement replication or quorum logic (delegated to `Coordinator`)
- Does not implement the sibling merging algorithm (delegated to `SiblingSet.Merge`)
- Does not implement vector clocks (delegated to `vclock`)
- Does not manage the RPC layer (delegated to `server`)

## 4. Architecture Design

```
+-------------------------------------+
|          storage.Engine             |
|                                     |
|  Get(key) → SiblingSet              |
|  Put(key, SiblingSet)               |
|  Merge(key, SiblingSet)             |  <- read-modify-write, used by anti-entropy
|  Delete(key)                        |  <- writes tombstone sibling
|  Scan(startKey, endKey) → iterator  |
|  TriggerCompaction()                |
|  DiskUsage() → DiskStats            |
|                                     |
|  +-------------------------------+  |
|  |     Pebble DB (LSM tree)      |  |
|  |  key: []byte (user key)       |  |
|  |  val: encoded SiblingSet      |  |
|  +-------------------------------+  |
|                                     |
|  TombstoneGC goroutine              |
|    every gc_interval:               |
|      scan all keys                  |
|      delete expired tombstones      |
+-------------------------------------+
```

## 5. Core Data Structures (Go)

```go
package storage

import (
    "time"
    "github.com/cockroachdb/pebble"
    "goquorum/internal/vclock"
)

// Engine is the primary interface for all local key-value operations.
type Engine interface {
    Get(key []byte) (*SiblingSet, error)
    Put(key []byte, sibs *SiblingSet) error
    Merge(key []byte, incoming *SiblingSet) error
    Delete(key []byte, vc *vclock.VClock) error
    Scan(startKey, endKey []byte) Iterator
    TriggerCompaction() error
    DiskUsage() DiskStats
    Close() error
}

// PebbleEngine implements Engine using Pebble as the backend.
type PebbleEngine struct {
    db      *pebble.DB
    config  EngineConfig
    gcStop  chan struct{}
    wg      sync.WaitGroup
    logger  *slog.Logger
    metrics *EngineMetrics
}

// Iterator is a forward-only cursor over a key range.
type Iterator interface {
    Next() bool
    Key() []byte
    Value() (*SiblingSet, error)
    Close()
}

// DiskStats holds storage space information.
type DiskStats struct {
    DiskUsageBytes  int64
    LiveKeyCount    int64
    TombstoneCount  int64
}

// EngineConfig holds all tunable parameters for the storage engine.
type EngineConfig struct {
    DataDir          string        `yaml:"data_dir"`
    SyncWrites       bool          `yaml:"sync_writes" default:"true"`
    CacheSizeMB      int           `yaml:"cache_size_mb" default:"256"`
    MemtableSizeMB   int           `yaml:"memtable_size_mb" default:"64"`
    MaxOpenFiles     int           `yaml:"max_open_files" default:"1000"`
    MaxKeySizeBytes  int           `yaml:"max_key_size_bytes" default:"65536"`
    MaxValueSizeMB   int           `yaml:"max_value_size_mb" default:"1"`
    TombstoneTTL     time.Duration `yaml:"tombstone_ttl" default:"168h"` // 7 days
    GCInterval       time.Duration `yaml:"gc_interval" default:"1h"`
    MaxSiblings      int           `yaml:"max_siblings" default:"100"`
}
```

## 6. Public Interfaces

```go
package storage

// NewPebbleEngine opens or creates a Pebble database at cfg.DataDir
// and starts the background GC goroutine.
func NewPebbleEngine(cfg EngineConfig, logger *slog.Logger) (*PebbleEngine, error)

// Get retrieves the SiblingSet for key.
// Returns ErrNotFound if the key does not exist.
func (e *PebbleEngine) Get(key []byte) (*SiblingSet, error)

// Put overwrites the SiblingSet for key unconditionally.
// Used by the Coordinator's replication path.
func (e *PebbleEngine) Put(key []byte, sibs *SiblingSet) error

// Merge reads the existing SiblingSet for key, merges incoming into it,
// and writes the result back. Used by anti-entropy and read repair.
// The merge is NOT atomic (not a Pebble Merge operator) — it uses a read-modify-write.
// The caller must hold the appropriate key lock if atomicity is required.
func (e *PebbleEngine) Merge(key []byte, incoming *SiblingSet) error

// Delete writes a tombstone sibling for key using the provided vector clock.
// The tombstone is later garbage-collected by TombstoneGC after TTL expires.
func (e *PebbleEngine) Delete(key []byte, vc *vclock.VClock) error

// Scan returns an Iterator over keys in [startKey, endKey).
// If endKey is nil, scans to the end of the key space.
func (e *PebbleEngine) Scan(startKey, endKey []byte) Iterator

// TriggerCompaction schedules a full manual compaction of the LSM tree.
func (e *PebbleEngine) TriggerCompaction() error

// DiskUsage returns current storage statistics.
func (e *PebbleEngine) DiskUsage() DiskStats

// Close flushes pending writes, stops the GC goroutine, and closes Pebble.
func (e *PebbleEngine) Close() error
```

## 7. Internal Algorithms

### Get
```
Get(key):
  validate key size (1–MaxKeySize)
  raw, closer, err = db.Get(key)
  if err == pebble.ErrNotFound: return nil, ErrNotFound
  if err != nil: return nil, err
  defer closer.Close()

  sibs, err = Decode(raw)
  if err != nil:
    metrics.DecodeError.Inc()
    return nil, err

  metrics.GetLatency.Observe(elapsed)
  return sibs, nil
```

### Put
```
Put(key, sibs):
  validate key/value size
  encoded, err = sibs.Encode()
  if err != nil: return err

  opts = pebble.WriteOptions{Sync: config.SyncWrites}
  err = db.Set(key, encoded, opts)
  metrics.PutLatency.Observe(elapsed)
  return err
```

### Merge
```
Merge(key, incoming):
  existing, err = Get(key)
  if err == ErrNotFound:
    existing = NewSiblingSet()
  elif err != nil:
    return err

  merged = existing.Merge(incoming, config.MaxSiblings)
  return Put(key, merged)
```

### Tombstone GC Loop
```
gcLoop():
  ticker = time.NewTicker(config.GCInterval)
  for:
    select:
    case <-ticker.C:
      gcRound()
    case <-e.gcStop:
      return

gcRound():
  iter = db.NewIter(nil)
  defer iter.Close()
  toDelete = [][]byte{}

  for iter.First(); iter.Valid(); iter.Next():
    sibs, err = Decode(iter.Value())
    if err != nil: continue

    if sibs.AllTombstones():
      oldest = sibs.OldestTombstoneTime()
      if time.Since(oldest) > config.TombstoneTTL:
        toDelete = append(toDelete, copyKey(iter.Key()))

  batch = db.NewBatch()
  for _, key in toDelete:
    batch.Delete(key, nil)
  batch.Commit(pebble.Sync)
  metrics.GCDeletedKeys.Add(len(toDelete))
```

## 8. Persistence Model

All data is stored in Pebble's LSM tree under `cfg.DataDir`. Pebble provides:
- **Write-ahead log (WAL)**: Durable writes when `SyncWrites=true`
- **Block cache**: In-memory cache for hot data, sized by `CacheSizeMB`
- **Memtable**: In-memory write buffer, flushed to L0 SSTs
- **Compaction**: Background LSM compaction to merge SSTs and reclaim space

Key format: raw user key bytes (1–64KB).
Value format: encoded `SiblingSet` binary (see `plan-sibling-set.md`).

There is no separate index file. All metadata (vector clocks, tombstone flags, timestamps) is encoded inline in the value.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Pebble's internal concurrency | Pebble is goroutine-safe; multiple goroutines can call `Get`/`Put` concurrently |
| Coordinator key lock | Caller holds per-key lock for `Merge` (read-modify-write) to prevent races |
| GC goroutine | Single background GC goroutine; reads in GC loop use a snapshot iterator and do not block writes |
| `sync.WaitGroup` in Close | Waits for GC goroutine to complete before closing Pebble |

## 10. Configuration

See `EngineConfig` in Section 5. Key defaults:

| Parameter | Default | Notes |
|-----------|---------|-------|
| `sync_writes` | `true` | Disable for higher throughput at durability risk |
| `cache_size_mb` | `256` | Increase for larger datasets |
| `tombstone_ttl` | `168h` | Must be > max cluster partition duration |
| `gc_interval` | `1h` | Reduce for faster tombstone reclamation |
| `max_siblings` | `100` | Protects against unbounded MVCC growth |

## 11. Observability

- `goquorum_storage_get_duration_seconds` — Get latency histogram
- `goquorum_storage_put_duration_seconds` — Put latency histogram
- `goquorum_storage_get_total{result="hit|miss|error"}` — Get outcome counters
- `goquorum_storage_put_total{result="success|error"}` — Put outcome counters
- `goquorum_storage_disk_usage_bytes` — Gauge of total Pebble disk usage
- `goquorum_storage_live_key_count` — Gauge of estimated live keys
- `goquorum_storage_tombstone_count` — Gauge of tombstone entries
- `goquorum_storage_gc_deleted_keys_total` — Counter of GC-deleted keys
- `goquorum_storage_gc_duration_seconds` — GC round duration histogram
- `goquorum_storage_checksum_errors_total` — Counter of CRC32 mismatches on decode
- Log at INFO at startup with data dir path and Pebble version
- Log at WARN when disk usage exceeds 80% of available space

## 12. Testing Strategy

- **Unit tests**:
  - `TestGetPutRoundTrip`: Put then Get, assert sibling set equality
  - `TestGetNotFound`: Get missing key, assert ErrNotFound
  - `TestMergeAddsNewSibling`: Merge incoming sibling with existing, assert both present
  - `TestMergeOverwritesOlderSibling`: incoming causally dominates existing, assert older removed
  - `TestDeleteCreatesTombstone`: Delete key, Get key, assert tombstone sibling present
  - `TestScanRange`: insert 100 keys, scan [key50, key75), assert correct keys returned
  - `TestGCDeletesExpiredTombstones`: insert tombstone with timestamp=now-8days, run GC, assert key deleted
  - `TestGCPreservesActiveTombstones`: insert tombstone with timestamp=now-1hour, run GC, assert key NOT deleted
  - `TestConcurrentReadsWrites`: 10 goroutines reading and writing concurrently, assert no data corruption
  - `TestTriggerCompaction`: call TriggerCompaction, assert no error
  - `TestChecksumCorruptionHandled`: corrupt stored value, assert Get returns error (not panic)
- **Integration tests**:
  - `TestPersistenceAcrossRestart`: write 1000 keys, close engine, reopen, assert all keys readable

## 13. Open Questions

None.
