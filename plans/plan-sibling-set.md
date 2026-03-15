# Feature: Sibling Set & MVCC

## 1. Purpose

The SiblingSet is GoQuorum's multi-version concurrency control (MVCC) layer. When concurrent writes to the same key occur without a causal relationship, GoQuorum cannot discard either — both are "siblings." The SiblingSet stores all causally unresolved versions together until a client reads and reconciles them (or until anti-entropy propagates a dominant version). This is the core mechanism that makes GoQuorum an AP system: it prefers availability over forcing a single winner at write time.

## 2. Responsibilities

- **Sibling storage**: Hold a set of `Sibling` values, each with its own vector clock and value bytes
- **Merge / add**: Accept a new sibling and discard any existing siblings that the new one causally dominates; preserve existing siblings that are concurrent with the new one
- **Tombstone handling**: Represent deletes as tombstone siblings; filter them from read results; track TTL for GC eligibility
- **Explosion protection**: Enforce a maximum sibling count by pruning the oldest siblings (by timestamp) when the limit is exceeded
- **Encoding / decoding**: Serialize the full sibling set to bytes for storage in Pebble; deserialize on read
- **Checksum**: Compute and verify a CRC32 checksum over the encoded sibling set for corruption detection

## 3. Non-Responsibilities

- Does not perform inter-node replication (delegated to `Coordinator`)
- Does not compare clocks between sibling sets from different replicas (delegated to `Coordinator.reconcile`)
- Does not implement vector clock logic (delegated to `vclock`)
- Does not manage Pebble transactions (delegated to `storage.Engine`)
- Does not implement tombstone GC scheduling (delegated to `TombstoneGC`)

## 4. Architecture Design

```
SiblingSet
  |
  +-- Sibling[0]:  VClock={A:3,B:1}  Value="foo"   Ts=T1
  +-- Sibling[1]:  VClock={A:2,B:3}  Value="bar"   Ts=T2  (concurrent with [0])
  +-- Sibling[2]:  VClock={A:1}      tombstone=true Ts=T0  (causally older, may be pruned)

After Add(new Sibling with VClock={A:4,B:3}):
  Sibling[0] dominated (A:4,B:3 >= A:3,B:1 in all entries) → removed
  Sibling[1] dominated (A:4,B:3 >= A:2,B:3 in all entries) → removed
  Sibling[2] dominated → removed
  Result: only new sibling remains
```

### Wire Encoding Layout
```
[uint16: sibling count]
for each sibling:
  [uint8: flags  (tombstone=0x01, compressed=0x02)]
  [int64: timestamp unix-ns]
  [uint16: vclock byte length]
  [bytes:  vclock binary]
  [uint32: value byte length]
  [bytes:  value]
[uint32: CRC32 checksum of all preceding bytes]
```

## 5. Core Data Structures (Go)

```go
package storage

import (
    "goquorum/internal/vclock"
    "hash/crc32"
    "time"
)

// Sibling is one version of a key's value, with its causal history.
type Sibling struct {
    VClock    *vclock.VClock
    Value     []byte      // nil for tombstone
    Timestamp time.Time   // wall clock at time of write
    Tombstone bool        // true if this is a delete marker
}

// SiblingSet is the MVCC value for a key: zero or more concurrent siblings.
type SiblingSet struct {
    siblings []*Sibling
}

const (
    flagTombstone  = 0x01
    flagCompressed = 0x02

    // DefaultMaxSiblings is the sibling explosion protection limit.
    DefaultMaxSiblings = 100
)
```

## 6. Public Interfaces

```go
package storage

// NewSiblingSet creates an empty sibling set.
func NewSiblingSet() *SiblingSet

// NewSibling creates a live (non-tombstone) sibling.
func NewSibling(value []byte, vc *vclock.VClock, ts time.Time) *Sibling

// NewTombstone creates a tombstone sibling (delete marker).
func NewTombstone(vc *vclock.VClock, ts time.Time) *Sibling

// Add merges newSib into the set:
//   - Removes existing siblings dominated by newSib
//   - Preserves siblings concurrent with newSib
//   - Adds newSib (unless an identical VClock already exists)
// Enforces maxSiblings limit after merge.
func (s *SiblingSet) Add(newSib *Sibling, maxSiblings int)

// Merge combines two SiblingSets by calling Add for each sibling in other.
// Used by the Coordinator to reconcile responses from multiple replicas.
func (s *SiblingSet) Merge(other *SiblingSet, maxSiblings int) *SiblingSet

// Siblings returns a snapshot of the current sibling slice.
func (s *SiblingSet) Siblings() []*Sibling

// FilterTombstones returns a new SiblingSet with tombstones removed.
// Used before returning to client — deletes appear as not-found.
func (s *SiblingSet) FilterTombstones() *SiblingSet

// IsEmpty returns true if the sibling set has no live (non-tombstone) siblings.
func (s *SiblingSet) IsEmpty() bool

// MaxVClock returns the element-wise merge of all sibling vector clocks.
// Used as the causal context to return to the client after a Get.
func (s *SiblingSet) MaxVClock() *vclock.VClock

// AllTombstones returns true if all siblings are tombstones (eligible for GC).
func (s *SiblingSet) AllTombstones() bool

// OldestTombstoneTime returns the oldest tombstone timestamp, or zero if none.
// Used by TombstoneGC to decide if the entry is past its TTL.
func (s *SiblingSet) OldestTombstoneTime() time.Time

// Encode serializes the sibling set to bytes (wire format + CRC32).
func (s *SiblingSet) Encode() ([]byte, error)

// Decode deserializes a sibling set from bytes, verifying the checksum.
func Decode(data []byte) (*SiblingSet, error)

// Len returns the number of siblings.
func (s *SiblingSet) Len() int
```

## 7. Internal Algorithms

### Add (Merge a New Sibling)
```
Add(newSib, maxSiblings):
  retained = []
  for each existing in s.siblings:
    rel = existing.VClock.Compare(newSib.VClock)
    if rel == HappensBefore:
      // existing is dominated; discard it
      continue
    if rel == Equal:
      // identical clock; deduplicate, keep existing
      return
    // HappensAfter or Concurrent: keep existing
    retained = append(retained, existing)

  retained = append(retained, newSib)

  // Explosion protection: keep newest maxSiblings by timestamp
  if len(retained) > maxSiblings:
    sort retained by Timestamp descending
    retained = retained[:maxSiblings]

  s.siblings = retained
```

### Merge (Two SiblingSets)
```
Merge(other, maxSiblings):
  result = copy(s)
  for each sib in other.siblings:
    result.Add(sib, maxSiblings)
  return result
```

### MaxVClock
```
MaxVClock():
  if len(s.siblings) == 0: return vclock.New()
  merged = s.siblings[0].VClock.Copy()
  for each sib in s.siblings[1:]:
    merged = merged.Merge(sib.VClock)
  return merged
```

### Encode
```
Encode():
  buf = bytes.Buffer{}
  write uint16(len(s.siblings))
  for each sib:
    flags = 0
    if sib.Tombstone: flags |= flagTombstone
    write uint8(flags)
    write int64(sib.Timestamp.UnixNano())
    vclockBytes = sib.VClock.MarshalBinary()
    write uint16(len(vclockBytes))
    write vclockBytes
    write uint32(len(sib.Value))
    write sib.Value
  checksum = crc32.ChecksumIEEE(buf.Bytes())
  write uint32(checksum)
  return buf.Bytes()
```

### Decode
```
Decode(data):
  verify len(data) >= 6 (at least count + checksum)
  stored_crc = last 4 bytes
  computed_crc = crc32.ChecksumIEEE(data[:len-4])
  if stored_crc != computed_crc: return ErrChecksumMismatch

  r = bytes.Reader(data)
  count = read uint16
  siblings = []
  for i in 0..count:
    flags = read uint8
    ts = read int64 → time.Time
    vclockLen = read uint16
    vcBytes = read vclockLen bytes
    vc = vclock.UnmarshalBinary(vcBytes)
    valueLen = read uint32
    value = read valueLen bytes
    sib = Sibling{VClock: vc, Value: value, Timestamp: ts, Tombstone: flags & flagTombstone != 0}
    siblings = append(siblings, sib)
  return SiblingSet{siblings: siblings}
```

## 8. Persistence Model

SiblingSets are stored as opaque byte blobs in Pebble. The key is the raw user key; the value is the encoded SiblingSet bytes (including checksum). Pebble provides atomicity and durability for individual key writes.

There is no separate index for sibling count or vector clocks. All metadata is embedded in the value encoding. On read, the full sibling set is decoded from bytes.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| No internal locking | `SiblingSet` is not goroutine-safe by itself |
| Coordinator key lock | The `Coordinator` holds a per-key mutex around all read-modify-write cycles on the SiblingSet |
| Immutable sibling values | Once created, a `Sibling`'s fields are never mutated |

The caller (`Coordinator` or `storage.Engine`) is responsible for synchronizing access to a `SiblingSet` for the same key. Different keys are fully independent.

## 10. Configuration

```go
type SiblingConfig struct {
    // MaxSiblings is the maximum number of siblings before explosion protection kicks in.
    MaxSiblings int `yaml:"max_siblings" default:"100"`
}
```

## 11. Observability

- `goquorum_sibling_count` — Histogram of sibling count per key on every Get (before reconciliation)
- `goquorum_sibling_explosion_total` — Counter incremented when siblings are pruned due to explosion limit
- `goquorum_sibling_set_bytes` — Histogram of encoded sibling set size in bytes
- `goquorum_checksum_error_total` — Counter of CRC32 checksum mismatches on decode (data corruption events)
- Log at WARN when sibling count exceeds `MaxSiblings / 2` (early warning)
- Log at ERROR when checksum mismatch detected, including key name

## 12. Testing Strategy

- **Unit tests**:
  - `TestAddDominatesExisting`: Add a causally later sibling, assert older one is removed
  - `TestAddConcurrentPreservesBoth`: Add two concurrent siblings, assert both exist
  - `TestAddDeduplicateEqualClock`: Add two siblings with identical vector clocks, assert only one kept
  - `TestMergeTwoSiblingSets`: two sets with 2 siblings each, merge, assert correct reconciliation
  - `TestFilterTombstones`: set with one tombstone and one live sibling, filter, assert only live returned
  - `TestAllTombstones`: all siblings are tombstones, assert AllTombstones() = true
  - `TestMaxVClock`: multiple siblings, assert MaxVClock is element-wise maximum
  - `TestExplosionProtection`: add 150 siblings, assert count capped at MaxSiblings (100)
  - `TestEncodeDecodeRoundTrip`: encode then decode, assert structural equality
  - `TestChecksumMismatchDetected`: corrupt one byte in encoded data, assert Decode returns error
  - `TestTombstoneEncoding`: tombstone sibling round-trips correctly with flag preserved
- **Integration tests**:
  - `TestConcurrentWritesSiblingResolution`: concurrent Puts without causal context, read back, assert siblings present until resolved

## 13. Open Questions

None.
