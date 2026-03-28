---
name: Vector Clocks
description: Lamport vector clocks for causality tracking, conflict detection, and sibling pruning
type: project
---

# Vector Clocks

## Overview

Vector clocks track causality across nodes. Every `Sibling` in the storage layer carries a `VectorClock`. When two siblings are compared, their clocks determine whether one supersedes the other (causal ordering) or whether they represent genuine concurrent writes (a conflict, producing siblings).

## Data Structures (`internal/vclock/vclock.go`)

**`VectorClockEntry`**:
```
NodeID     string
Counter    uint64    — Lamport logical timestamp
Timestamp  int64     — Unix seconds; used only for pruning decisions, not ordering
```

**`VectorClock`**: `map[NodeID]*VectorClockEntry`. Internally maintained as a map; operations serialize to a sorted slice for stable encoding.

## Core Operations

| Function | Signature | Behavior |
|---|---|---|
| `Tick` | `Tick(nodeID string)` | Increments `Counter[nodeID]` by 1; sets `Timestamp[nodeID]` to `time.Now().Unix()` |
| `Get` | `Get(nodeID string) uint64` | Returns counter (0 if absent) |
| `Set` | `Set(nodeID string, counter uint64)` | Sets counter and updates timestamp |
| `Merge` | `Merge(other *VectorClock)` | Per-node max of counters; on tie, keeps newer `Timestamp` |
| `Copy` | `Copy() *VectorClock` | Deep copy (independent mutation) |

## Causal Comparison (`internal/vclock/compare.go`)

`Compare(vc1, vc2 *VectorClock) Ordering` returns one of:

| Ordering | Meaning | Condition |
|---|---|---|
| `Before` | vc1 happens-before vc2 | `∀i: vc1[i] ≤ vc2[i]` and `∃i: vc1[i] < vc2[i]` |
| `After` | vc1 happens-after vc2 | `∀i: vc1[i] ≥ vc2[i]` and `∃i: vc1[i] > vc2[i]` |
| `Equal` | Identical clocks | `∀i: vc1[i] == vc2[i]` |
| `Concurrent` | Neither dominates | `∃i: vc1[i] < vc2[i]` and `∃j: vc1[j] > vc2[j]` |

Algorithm: scans the union of node IDs from both clocks, tracking whether either clock has any strictly lower entry (`less`, `greater`). Return value derived from `(less, greater)` pair.

### High-Level Predicates

```go
HappensAfter(other)        // Compare == After
HappensBefore(other)       // Compare == Before
IsConcurrentWith(other)    // Compare == Concurrent
Equals(other)              // Compare == Equal
Dominates(other)           // After OR Equal  (supersedes other)
IsDominatedBy(other)       // Before OR Equal
CanCauseConflict(other)    // Concurrent OR Equal
```

`HappensAfter` is the primary predicate used by `reconcileSiblings` in the storage engine to decide whether an existing sibling is superseded by an incoming one.

## Pruning

Over time, a vector clock accumulates entries for every node that has touched a key. `Prune(threshold time.Duration, maxEntries int)` applies two passes:

1. **Age-based**: Remove entries where `time.Now().Unix() - entry.Timestamp > threshold` (default 7 days).
2. **Count-based**: If more than `maxEntries` (default 50) remain, keep only the `maxEntries` most recent by `Timestamp`.

Returns the count of pruned entries. Invoked in `Storage.Put` after sibling reconciliation.

## Serialization

**Binary format** (`MarshalBinary` / `UnmarshalBinary`):
```
[entry_count: 2 bytes, LE]
For each entry (sorted by NodeID, deterministic):
  [node_id_len: 2 bytes, LE]
  [node_id:     bytes]
  [counter:     8 bytes, LE]
  [timestamp:   8 bytes, LE]
```
Used inside `encodeSiblingSet` when writing individual sibling clocks to Pebble.

**Compact JSON** (`MarshalJSON`): `{"node1": 5, "node2": 3}` — counter only, no timestamp. Used in client API responses (`VClockContext`).

**Detailed JSON** (`MarshalDetailed`): `{"entries": [{"node_id": "n1", "counter": 5, "timestamp": 1704067200}]}` — includes timestamps; used in debug/diagnostic output.

**String representation**: `[node1:5@1704067200, node2:3@1704067100]` (sorted, timestamp as decimal Unix seconds).

## Use Across the System

| Site | Usage |
|---|---|
| `Storage.reconcileSiblings` | `HappensAfter` to drop superseded existing siblings |
| `Coordinator.Put` | `Tick(nodeID)` on write to advance local counter |
| `Coordinator.mergeAllSiblings` | `Merge` across all read responses to produce canonical clock |
| `ReadRepairer.checkNeedsRepair` | `IsDominatedBy` / `Equals` to detect stale replica |
| `VectorClock.Prune` (in `Storage.Put`) | Age+count trimming to bound clock size |
| `TTLSweeper.sweep` | `Merge` all sibling clocks, then `Tick` before tombstone delete |
| `quorumctl` CLI | Base64(JSON) encoding for human-passable context tokens |

Changes:

