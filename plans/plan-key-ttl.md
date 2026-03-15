# Feature: Key TTL / Expiry

## 1. Purpose

GoQuorum currently supports only explicit deletes for key removal. Many use cases — session storage, ephemeral locks, cache entries, rate-limit counters — require keys that automatically expire after a client-specified duration. Key TTL allows clients to set an expiration time on Put, after which the key behaves as if it were deleted. The expiry is enforced lazily on reads (return not-found if expired) and eagerly by a background sweeper. This feature is not yet implemented.

## 2. Responsibilities

- **TTL on Put**: Accept an optional TTL duration (or absolute expiry timestamp) in the Put request; store it alongside the sibling
- **Read-time expiry check**: On Get, if the key's TTL has elapsed, return ErrNotFound (regardless of sibling content)
- **Write-time TTL propagation**: Include the expiry timestamp in the sibling set so it replicates to all N nodes and every replica enforces the same expiry
- **TTL sweeper**: Background goroutine that scans for expired keys and writes tombstones for them (allowing normal tombstone GC to physically remove them)
- **Proto and API extensions**: Add a `ttl_seconds` field to `PutRequest` in the protobuf definition

## 3. Non-Responsibilities

- Does not implement wall-clock synchronization across nodes — expiry uses the timestamp embedded in the sibling, not the reading node's clock; clock skew may cause ±seconds difference in expiry time
- Does not support TTL on individual siblings — TTL applies to the key as a whole
- Does not support TTL extension (refresh) without a new Put
- Does not implement Redis-style `EXPIRE`/`PERSIST` commands as separate RPCs

## 4. Architecture Design

```
Client: Put("session:abc", value, ttl=30m)
    |
    v
Coordinator.Put:
  newSib.ExpiresAt = now() + 30m
  replicate to N nodes (ExpiresAt embedded in sibling encoding)

Coordinator.Get:
  sibs = reconcile(N replicas)
  if sibs.IsExpired(now()):
    return ErrNotFound
  return sibs

TTLSweeper goroutine (every sweep_interval):
  scan all keys
  for each key where sibs.IsExpired(now()):
    coordinator.Delete(key, sibs.MaxVClock())  // writes tombstone
```

### ExpiresAt in Sibling Encoding
```
Sibling {
  VClock    *vclock.VClock
  Value     []byte
  Timestamp time.Time
  Tombstone bool
  ExpiresAt time.Time  // zero = no TTL
}
```

## 5. Core Data Structures (Go)

```go
package storage

// Sibling is extended to include an optional expiry time.
// ExpiresAt zero value means no TTL.
type Sibling struct {
    VClock    *vclock.VClock
    Value     []byte
    Timestamp time.Time
    Tombstone bool
    ExpiresAt time.Time // zero = no expiry
}

// SiblingSet gains an expiry-aware method.
// (IsExpired is based on the minimum ExpiresAt across all non-tombstone siblings,
// since siblings represent concurrent versions of the same key.)

package cluster

// TTLSweeper scans for expired keys and writes tombstones.
type TTLSweeper struct {
    config   TTLSweeperConfig
    engine   storage.Engine
    coord    *Coordinator
    stopCh   chan struct{}
    wg       sync.WaitGroup
    logger   *slog.Logger
    metrics  *TTLMetrics
}

// TTLSweeperConfig holds tuning parameters.
type TTLSweeperConfig struct {
    Enabled       bool          `yaml:"enabled" default:"true"`
    SweepInterval time.Duration `yaml:"sweep_interval" default:"1m"`
    BatchSize     int           `yaml:"batch_size" default:"1000"`
}
```

## 6. Public Interfaces

```go
package storage

// IsExpired returns true if all non-tombstone siblings in the set have a non-zero
// ExpiresAt that has passed as of `now`.
func (s *SiblingSet) IsExpired(now time.Time) bool

// TTLForPut is an option for Coordinator.Put.
type TTLForPut time.Duration

package cluster

// PutOption is a functional option for Coordinator.Put.
type PutOption func(*putConfig)

// WithTTL sets an expiration TTL on the written key.
func WithTTL(ttl time.Duration) PutOption {
    return func(c *putConfig) { c.ttl = ttl }
}

// Coordinator.Put is extended to accept PutOptions:
func (c *Coordinator) Put(ctx context.Context, key, value []byte, opts ...PutOption) (*vclock.VClock, error)

// Proto extension (goquorum.proto):
// message PutRequest {
//   string key   = 1;
//   bytes  value = 2;
//   bytes  context = 3;
//   uint32 ttl_seconds = 4;  // 0 = no TTL
// }

// NewTTLSweeper creates a TTLSweeper.
func NewTTLSweeper(cfg TTLSweeperConfig, eng storage.Engine, coord *Coordinator, logger *slog.Logger) *TTLSweeper

// Start launches the background sweep goroutine.
func (s *TTLSweeper) Start()

// Stop shuts down the sweeper.
func (s *TTLSweeper) Stop()
```

## 7. Internal Algorithms

### Coordinator.Put with TTL
```
Put(ctx, key, value, opts...):
  cfg = applyPutOpts(opts)

  vc = local.GetVClock(key) or vclock.New()
  vc.Tick(nodeID)

  expiresAt = time.Time{}  // zero = no TTL
  if cfg.ttl > 0:
    expiresAt = now().Add(cfg.ttl)

  newSib = storage.NewSiblingWithTTL(value, vc, now(), expiresAt)
  // ... fanout to N replicas as before ...
```

### Coordinator.Get with Expiry Check
```
Get(ctx, key):
  // ... collect R responses, reconcile ...
  merged = reconcile(collected...)

  // Check expiry before filtering tombstones
  if merged.IsExpired(now()):
    // Trigger async tombstone write to clean up
    go c.deleteExpired(ctx, key, merged.MaxVClock())
    return nil, nil, ErrNotFound

  merged = merged.FilterTombstones()
  if merged.IsEmpty():
    return nil, nil, ErrNotFound
  // ...
```

### SiblingSet.IsExpired
```
IsExpired(now):
  for each sib in s.siblings:
    if sib.Tombstone: continue
    if sib.ExpiresAt.IsZero(): return false  // at least one sibling has no TTL
    if sib.ExpiresAt.After(now): return false  // at least one sibling is still valid
  // All non-tombstone siblings have passed their ExpiresAt
  return true
```

### TTLSweeper.sweep
```
sweep():
  iter = engine.Scan(nil, nil)
  defer iter.Close()
  now = time.Now()

  expired = [][]byte{}
  for iter.Next():
    sibs, err = iter.Value()
    if err != nil: continue
    if sibs.IsExpired(now):
      expired = append(expired, copyBytes(iter.Key()))

  // Write tombstones for expired keys (this is a local operation only;
  // the tombstone will replicate and trigger GC on all nodes)
  for _, key in expired:
    existing, _ = engine.Get(key)
    vc = existing.MaxVClock()
    vc.Tick(localNodeID)
    engine.Put(key, SiblingSet{NewTombstone(vc, now)})
    metrics.ExpiredKeysTombstoned.Inc()
```

### Sibling Binary Encoding (extended)
```
Existing flags byte:
  bit 0: tombstone
  bit 1: compressed
  bit 2: has_expiry  (NEW)

If has_expiry flag set:
  after value bytes, append int64 ExpiresAt (unix nanoseconds)
```

## 8. Persistence Model

The `ExpiresAt` timestamp is stored inline in the sibling binary encoding as an optional int64 field (flagged by a bit in the existing flags byte). This requires a minor, backward-compatible extension to the sibling encoding format. Existing entries without the has_expiry flag are treated as having no TTL.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Immutable ExpiresAt | Set at write time and never modified; safe for concurrent reads |
| TTLSweeper single goroutine | One sweep at a time; uses a storage scan snapshot |
| Coordinator key lock | Not held during sweeper tombstone writes (sweeper calls coordinator.Put which acquires the key lock) |

## 10. Configuration

```go
type TTLSweeperConfig struct {
    Enabled       bool          `yaml:"enabled" default:"true"`
    SweepInterval time.Duration `yaml:"sweep_interval" default:"1m"`
    BatchSize     int           `yaml:"batch_size" default:"1000"`
}
```

TTL accuracy: keys are guaranteed to appear expired on reads immediately after `ExpiresAt` passes. Physical removal (via tombstone GC) happens within `sweep_interval + tombstone_ttl` of expiry. For most workloads, this is acceptable.

## 11. Observability

- `goquorum_ttl_expired_reads_total` — Counter of Get requests that returned ErrNotFound due to TTL expiry
- `goquorum_ttl_sweep_expired_keys_total` — Counter of keys tombstoned by the sweeper
- `goquorum_ttl_sweep_duration_seconds` — Histogram of sweep scan duration
- Log at DEBUG on each sweep with count of expired keys found and tombstoned

## 12. Testing Strategy

- **Unit tests**:
  - `TestPutWithTTLSetsExpiresAt`: Put with TTL=1m, assert sibling.ExpiresAt ≈ now()+1m
  - `TestGetReturnsNotFoundWhenExpired`: insert key with ExpiresAt=past, Get, assert ErrNotFound
  - `TestGetSucceedsBeforeExpiry`: insert key with ExpiresAt=future, Get, assert success
  - `TestGetNoTTLNeverExpires`: insert key without TTL, assert IsExpired always false
  - `TestIsExpiredAllSiblings`: two siblings both expired, assert IsExpired=true
  - `TestIsExpiredOneSiblingValid`: one expired + one valid sibling, assert IsExpired=false
  - `TestSiblingEncodingWithTTL`: encode/decode round-trip for sibling with ExpiresAt set
  - `TestSiblingEncodingWithoutTTL`: existing encoding format unchanged for zero ExpiresAt
  - `TestTTLSweeperTombstonesExpiredKey`: insert expired key, run sweeper, assert tombstone written
  - `TestTTLSweeperSkipsValidKeys`: insert unexpired key, run sweeper, assert key NOT tombstoned
- **Integration tests**:
  - `TestTTLEndToEnd`: Put with TTL=2s, sleep 3s, Get, assert ErrNotFound
  - `TestTTLPropagatesAcrossReplicas`: 3-node cluster, Put with TTL, verify all replicas expire at same time

## 13. Open Questions

- Should TTL be per-sibling or per-key? Currently per-key (all siblings of a key share the expiry semantics from the latest write). Per-sibling TTL would be more complex but allow partial expiry of concurrent writes.
- How should TTL interact with sibling conflicts? If two concurrent Puts set different TTLs, the reconciled key inherits both. The `IsExpired` check keeps the key alive as long as any non-tombstone sibling is valid.
