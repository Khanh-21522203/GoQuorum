# Feature: Vector Clocks

## 1. Purpose

Vector clocks are the causal consistency primitive of GoQuorum. Each write carries a vector clock that encodes the causal history of that value across all nodes that have participated in writing it. By comparing vector clocks, GoQuorum can determine whether two versions of a key are causally ordered (one happened before the other) or concurrent (neither dominates), enabling correct conflict detection and sibling set management without a central sequencer.

## 2. Responsibilities

- **Clock representation**: Maintain a map from node ID to (logical counter, wall timestamp) per entry
- **Tick**: Increment the local node's logical counter on every write, advancing causal time
- **Comparison**: Determine the causal relationship between two vector clocks: `HappensBefore`, `HappensAfter`, `Concurrent`, `Equal`
- **Merge**: Produce a new clock that is the element-wise maximum of two clocks (for read repair and reconciliation)
- **Pruning**: Remove stale entries by age or count to bound clock size growth as nodes join and leave
- **Serialization**: Binary encode/decode clocks for on-disk storage and wire transmission; JSON encode/decode for debug APIs

## 3. Non-Responsibilities

- Does not manage sibling sets (delegated to `storage.SiblingSet`)
- Does not coordinate across nodes (delegated to `Coordinator`)
- Does not persist clocks directly — clocks are always stored as part of a `Sibling`
- Does not assign node IDs (caller provides node ID string)

## 4. Architecture Design

```
+-----------------------------+
|        VClock               |
|                             |
|  entries: map[NodeID]Entry  |
|                             |
|  Entry {                    |
|    Counter   uint64         |
|    Timestamp int64 (unix ns)|
|  }                          |
+-----------------------------+
       |            |
  Tick()         Compare(other)
  Merge(other)   HappensBefore / After / Concurrent / Equal
  Prune(rules)
  MarshalBinary() / UnmarshalBinary()
  MarshalJSON()   / UnmarshalJSON()
```

### Causal Ordering Rules

Given two clocks A and B:
- `A HappensBefore B` — every entry in A is ≤ the corresponding entry in B, and at least one is strictly less
- `A HappensAfter B` — symmetric
- `A Concurrent B` — neither happens before the other
- `A Equal B` — all entries identical

## 5. Core Data Structures (Go)

```go
package vclock

import (
    "encoding/binary"
    "fmt"
    "sort"
    "time"
)

// NodeID is the unique identifier for a node in the cluster.
type NodeID = string

// Entry holds the logical counter and wall-clock timestamp for one node's contribution.
type Entry struct {
    Counter   uint64 // monotonically increasing logical counter
    Timestamp int64  // unix nanoseconds at time of tick, for pruning
}

// VClock is an immutable-safe vector clock mapping NodeID → Entry.
// All mutation methods return a new VClock (copy-on-write).
type VClock struct {
    entries map[NodeID]Entry
}

// Relation represents the causal relationship between two vector clocks.
type Relation int

const (
    HappensBefore Relation = iota // A < B
    HappensAfter                  // A > B
    Concurrent                    // A || B
    Equal                         // A == B
)

// PruneConfig controls automatic clock size management.
type PruneConfig struct {
    // MaxEntries: prune oldest entries (by Timestamp) when count exceeds this.
    MaxEntries int // default 50
    // MaxAge: prune entries whose Timestamp is older than MaxAge.
    MaxAge time.Duration // default 0 (disabled)
}
```

## 6. Public Interfaces

```go
package vclock

// New creates an empty vector clock.
func New() *VClock

// Tick returns a new VClock with nodeID's counter incremented by 1
// and its timestamp set to now.
func (v *VClock) Tick(nodeID NodeID) *VClock

// Merge returns a new VClock that is the element-wise maximum of v and other.
// Used for reconciling clocks across replicas.
func (v *VClock) Merge(other *VClock) *VClock

// Compare returns the causal relationship of v relative to other.
func (v *VClock) Compare(other *VClock) Relation

// HappensBefore returns true if v causally precedes other.
func (v *VClock) HappensBefore(other *VClock) bool

// HappensAfter returns true if v causally follows other.
func (v *VClock) HappensAfter(other *VClock) bool

// Concurrent returns true if v and other are causally unrelated.
func (v *VClock) Concurrent(other *VClock) bool

// Prune returns a new VClock with entries pruned according to cfg.
func (v *VClock) Prune(cfg PruneConfig) *VClock

// Get returns the Entry for nodeID, or zero if absent.
func (v *VClock) Get(nodeID NodeID) Entry

// Len returns the number of node entries in this clock.
func (v *VClock) Len() int

// Copy returns a deep copy of the vector clock.
func (v *VClock) Copy() *VClock

// MarshalBinary encodes the VClock into a compact binary format.
func (v *VClock) MarshalBinary() ([]byte, error)

// UnmarshalBinary decodes a VClock from binary format.
func UnmarshalBinary(data []byte) (*VClock, error)

// MarshalJSON encodes the VClock as a JSON object (for debug APIs).
func (v *VClock) MarshalJSON() ([]byte, error)

// UnmarshalJSON decodes a VClock from JSON.
func UnmarshalJSON(data []byte) (*VClock, error)
```

## 7. Internal Algorithms

### Tick
```
Tick(nodeID):
  newEntries = copy(v.entries)
  prev = newEntries[nodeID]
  newEntries[nodeID] = Entry{
    Counter:   prev.Counter + 1,
    Timestamp: now().UnixNano(),
  }
  return VClock{entries: newEntries}
```

### Merge
```
Merge(other):
  newEntries = copy(v.entries)
  for nodeID, entry in other.entries:
    existing = newEntries[nodeID]
    if entry.Counter > existing.Counter:
      newEntries[nodeID] = entry
  return VClock{entries: newEntries}
```

### Compare
```
Compare(other):
  vDominates = false
  otherDominates = false

  // Check all nodes in v
  for nodeID, vEntry in v.entries:
    otherEntry = other.entries[nodeID]  // defaults to {0, 0} if absent
    if vEntry.Counter > otherEntry.Counter:
      vDominates = true
    elif vEntry.Counter < otherEntry.Counter:
      otherDominates = true

  // Check nodes in other not in v
  for nodeID, otherEntry in other.entries:
    if nodeID not in v.entries and otherEntry.Counter > 0:
      otherDominates = true

  if vDominates && !otherDominates: return HappensAfter
  if otherDominates && !vDominates: return HappensBefore
  if !vDominates && !otherDominates: return Equal
  return Concurrent
```

### Prune
```
Prune(cfg):
  entries = copy(v.entries)
  cutoff = now() - cfg.MaxAge

  // Age-based pruning
  if cfg.MaxAge > 0:
    for nodeID, entry in entries:
      if entry.Timestamp < cutoff.UnixNano():
        delete(entries, nodeID)

  // Count-based pruning: keep MaxEntries newest by Timestamp
  if cfg.MaxEntries > 0 && len(entries) > cfg.MaxEntries:
    sorted = sortByTimestampAscending(entries)
    for i in 0..(len(sorted) - cfg.MaxEntries):
      delete(entries, sorted[i].NodeID)

  return VClock{entries: entries}
```

### Binary Encoding Format
```
[uint16: entry count]
for each entry (sorted by NodeID for determinism):
  [uint16: nodeID length]
  [bytes: nodeID]
  [uint64: Counter]
  [int64: Timestamp]
```
Total overhead: 2 + N*(2 + len(nodeID) + 16) bytes.

## 8. Persistence Model

Vector clocks are not stored independently. They are always embedded within a `Sibling` struct, which is part of a `SiblingSet` serialized by `storage.Encoding`. The binary format is stable across versions; future format changes require a version byte prefix.

The JSON format is human-readable and used only in debug/admin API responses. It is not used for storage.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Immutable copy-on-write | All mutation methods (`Tick`, `Merge`, `Prune`) return a new `*VClock`; the original is never modified |
| No locks required | Immutability means `*VClock` values can be shared across goroutines safely |

Callers are responsible for coordinating access to the variable holding a `*VClock` pointer if they need to mutate it atomically (e.g., via `atomic.Pointer[VClock]`).

## 10. Configuration

```go
type VClockConfig struct {
    // MaxEntries is the maximum number of node entries before pruning.
    MaxEntries int `yaml:"vclock_max_entries" default:"50"`
    // MaxAge is the maximum age of an entry before pruning. 0 disables age pruning.
    MaxAge time.Duration `yaml:"vclock_max_age" default:"0"`
}
```

## 11. Observability

- `goquorum_vclock_entries` — Histogram of vector clock entry count per sibling (to detect growth)
- `goquorum_vclock_prune_total` — Counter of pruning events (by reason: `age`, `count`)
- Log at DEBUG when a clock is pruned, including before/after entry counts
- Vector clock sizes are surfaced in admin `KeyInfo` response for debugging

## 12. Testing Strategy

- **Unit tests**:
  - `TestTickIncrements`: tick same node twice, assert counter = 2
  - `TestTickPreservesOtherNodes`: tick node A, assert node B's counter unchanged
  - `TestMergeElementWiseMax`: two clocks with overlapping and distinct nodes, assert merge takes max per node
  - `TestCompareHappensBefore`: A happened before B, assert `A.Compare(B) == HappensBefore`
  - `TestCompareHappensAfter`: symmetric
  - `TestCompareConcurrent`: two concurrent clocks, assert `Concurrent`
  - `TestCompareEqual`: identical clocks, assert `Equal`
  - `TestPruneByCount`: clock with 60 entries, prune to 50, assert 50 remain (newest by timestamp)
  - `TestPruneByAge`: add entries at old timestamps, prune, assert old entries removed
  - `TestBinaryRoundTrip`: marshal then unmarshal, assert equality
  - `TestJSONRoundTrip`: JSON encode then decode, assert equality
  - `TestDeterministicBinaryEncoding`: two identical clocks encoded independently, assert byte-for-byte equal
- **Integration tests**:
  - `TestCausalOrderAcrossNodes`: simulate 3-node cluster Put sequence, verify vector clocks form a valid causal chain

## 13. Open Questions

None.
