---
name: Storage Engine
description: Pebble LSM-backed persistence layer with sibling sets, vector clock reconciliation, TTL, and tombstone GC
type: project
---

# Storage Engine

## Overview

The storage engine persists key-value data using CockroachDB's Pebble LSM-tree. It is not a plain key-value store: each logical key maps to a `SiblingSet` — a set of concurrent versions that arose from conflicting writes. Vector clocks determine which versions supersede others; only truly concurrent writes coexist as siblings.

## Data Model

**`SiblingSet`** (`internal/storage/engine.go`): Slice of `*Sibling` pointers. Multiple siblings indicate a conflict; a single sibling is the common case.

**`Sibling`** struct:
```
Value      []byte    — raw payload (up to 1 MB)
VClock     VectorClock
Timestamp  int64     — Unix seconds (last-writer-wins tiebreak)
Tombstone  bool      — marks deletion
ExpiresAt  int64     — Unix seconds; 0 means no TTL
```

## Persistence (Pebble)

`NewStorage(opts StorageOptions)` (`engine.go:NewStorage`) opens a Pebble DB with:
- Block cache: `opts.BlockCacheSize` (default 256 MB, configurable as `CacheSizeMB` in YAML)
- MemTable: `opts.MemTableSize` (default 64 MB)
- Compaction concurrency: `opts.CompactionConcurrency` (default 1)
- Sync writes: `opts.SyncWrites` (default true; `HighThroughputOptions` disables for throughput at durability risk)

The Pebble key is the raw string key (byte slice). The value is an encoded `SiblingSet` (see Wire Format below).

## Wire Format (`internal/storage/encoding.go`)

Binary format with CRC32 IEEE checksum:

```
[sibling_count: 2 bytes, LE]
For each sibling:
  [vclock_len:  4 bytes, LE]
  [vclock:      protobuf bytes, vclock_len bytes]
  [flags:       1 byte]  — bit0=Tombstone, bit1=Compressed, bit2=TTL
  [timestamp:   8 bytes, LE]
  [expires_at:  8 bytes, LE]  — present only if FlagTTL set
  [value_len:   4 bytes, LE]
  [value:       value_len bytes]
[crc32:         4 bytes, LE]  — IEEE checksum over all preceding bytes
```

Constants: `MagicNumber = 0x47515243` ("GQRC"), `FormatVersion = 0x01`, `MaxKeySize = 65536` (64 KB), `MaxValueSize = 1048576` (1 MB), `MaxSiblings = 65535`.

A CRC32 mismatch returns `ErrCorruptedData`.

## Read Path

`Storage.Get(key)` returns a filtered `SiblingSet`:
1. Reads bytes from Pebble.
2. Validates CRC32 and decodes.
3. Filters expired siblings (`expiresAt > 0 && expiresAt <= now`).
4. Filters tombstone siblings (tombstones removed from client-facing results).

`Storage.GetRaw(key)` skips tombstone and expiry filtering — used by anti-entropy and read repair to see the full state.

## Write Path

`Storage.Put(key, incoming SiblingSet)`:
1. Loads existing `SiblingSet` via `GetRaw`.
2. Calls `reconcileSiblings(existing, incoming)`:
   - For each existing sibling, checks `HappensAfter(incoming sibling)`.
   - Drops existing siblings that are superseded by any incoming sibling.
   - Retains existing siblings not dominated by any incoming sibling (genuine concurrency).
   - Merges lists: non-superseded existing + all incoming.
3. Prunes if `len > opts.MaxSiblings` via `pruneSiblings()` (keeps newest by timestamp; emits `SiblingsPruned` metric, increments `SiblingExplosion` counter if pruning occurred).
4. Prunes vector clock entries older than `VClockPruneThreshold` (7 days) and caps at `VClockMaxEntries` (50).
5. Encodes and writes to Pebble (sync if `SyncWrites=true`).

`Storage.Delete(key, vclock)` constructs a tombstone `Sibling` and delegates to `Put`.

## Tombstone GC

Background goroutine (`tombstoneGCLoop`) runs every `TombstoneGCInterval` (default 1 h):
- Scans all keys via `Storage.Scan`.
- For each key whose every sibling is a tombstone older than `TombstoneTTL` (7 days): deletes the Pebble key entirely.
- Reuses `Put` if partial (some siblings are live, some are old tombstones).
- Emits `TombstonesGCed` counter.

Start condition: `opts.TombstoneGCEnabled` (default true).

## Scan

`Storage.Scan(start, end []byte, fn ScanFunc)` iterates Pebble keys in `[start, end)` order, calling `fn(key, siblingSet)` for each. Used by anti-entropy (`Build` the Merkle tree) and TTL sweeper.

## Error Mapping

| Pebble error | Domain error | Metric |
|---|---|---|
| `syscall.ENOSPC` | `ErrStorageFull` | `DiskFullErrors` |
| `syscall.EIO` | `ErrStorageIO` | `IOErrors` |
| CRC mismatch | `ErrCorruptedData` | `CorruptedReads` |

## Configuration (`internal/config/storage.go`, `internal/storage/options.go`)

| YAML field | StorageOptions field | Default |
|---|---|---|
| `cache_size_mb` | `BlockCacheSize` | 256 MB |
| `memtable_mb` | `MemTableSize` | 64 MB |
| `sync_writes` | `SyncWrites` | true |
| `max_siblings` | `MaxSiblings` | 100 |
| `tombstone_gc_interval` | `TombstoneGCInterval` | 1 h |
| `tombstone_ttl_days` | `TombstoneTTL` | 7 d |
| `vclock_prune_days` | `VClockPruneThreshold` | 7 d |
| `vclock_max_entries` | `VClockMaxEntries` | 50 |

Three preset builders: `DefaultStorageOptions()`, `HighThroughputOptions()` (sync off, 1 GB cache), `MemoryConstrainedOptions()` (64 MB cache).

## Metrics (`internal/storage/metric.go`)

`StorageMetrics` registered on a Prometheus registry:
- `ReadLatency` histogram (10 µs–10 s buckets)
- `WriteLatency` histogram (100 µs–100 s buckets)
- `BytesRead`, `BytesWritten`, `KeysTotal` counters
- `ReadErrors`, `WriteErrors`, `CorruptedReads` counters
- `SiblingsPruned`, `SiblingExplosion` counters
- `TombstonesGCed`, `DiskFullErrors`, `IOErrors` counters
- `VClockEntriesPruned` counter

## System Flow

```
Client write
     │
     ▼
Storage.Put(key, incomingSiblings)
     │
     ├─ GetRaw(key) ──► Pebble.Get → decode → raw SiblingSet
     │
     ├─ reconcileSiblings(existing, incoming)
     │     • HappensAfter per sibling pair
     │     • Remove superseded, keep concurrent
     │
     ├─ pruneSiblings (if count > MaxSiblings)
     │
     ├─ vclock.Prune (age + max entries)
     │
     └─ encode → CRC32 → Pebble.Set
```

Changes:

