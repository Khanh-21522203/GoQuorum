---
name: TTL & Tombstone GC
description: Key expiration via TTLSweeper and background garbage collection of old tombstones
type: project
---

# TTL & Tombstone GC

## Overview

Two complementary mechanisms clean up expired data:

1. **TTL Sweeper** (`internal/cluster/ttl_sweeper.go`) — scans for siblings with `ExpiresAt > 0` that have passed their expiry time and issues tombstone deletes through the coordinator (quorum-aware).
2. **Tombstone GC** (`internal/storage/engine.go:tombstoneGCLoop`) — removes tombstone-only keys from Pebble after they have aged past `TombstoneTTL`, reclaiming disk space.

These two work in sequence: TTL expiry creates tombstones; tombstone GC later removes them.

## TTL Sweeper

### Struct (`ttl_sweeper.go`)

```
storage     Storage
coordinator *Coordinator
interval    time.Duration   — sweep frequency (default: configurable)
stopCh      chan struct{}
wg          sync.WaitGroup
```

### Sweep Rate Limit

```go
const ttlSweepMaxPerRound = 1000
```

At most 1000 keys expire per sweep cycle to prevent flooding the cluster with tombstone writes during a large-scale expiration event.

### Sweep Logic (`sweep()`)

```
now = time.Now().Unix()
deleted = 0
storage.Scan("", "", fn):
  for each key, siblingSet:
    for each sibling in siblingSet:
      if sibling.ExpiresAt > 0 && sibling.ExpiresAt <= now:
          // Key or at least one sibling has expired
          mergedVClock = merge all sibling clocks, tick at localNodeID
          coordinator.Delete(ctx with 5s timeout, key, mergedVClock)
          deleted++
          if deleted >= ttlSweepMaxPerRound: return
          break (first expired sibling per key triggers delete)
```

The delete goes through `Coordinator.Delete`, which writes a tombstone with W-quorum, ensuring the expiration is replicated. The merged+ticked vector clock supersedes all existing siblings.

### TTL Assignment

TTL is set during `Put` by passing `ttlSeconds` option to `Coordinator.Put`. The coordinator sets `sibling.ExpiresAt = time.Now().Unix() + ttlSeconds` before writing. Lazy expiry also applies: `Storage.Get` filters siblings where `ExpiresAt <= now` before returning to clients.

## Tombstone GC

### Loop (`storage.go:tombstoneGCLoop`)

Runs every `TombstoneGCInterval` (default 1h):

```
storage.Scan("", "", fn):
  for each key, siblingSet (raw, unfiltered):
    if ALL siblings are tombstones AND all older than TombstoneTTL (7d):
        pebble.Delete(key)
        metrics.TombstonesGCed++
    elif SOME siblings are tombstones older than TombstoneTTL:
        filtered = remove old tombstones from siblingSet
        pebble.Set(key, encode(filtered))
```

Condition: only enabled when `StorageOptions.TombstoneGCEnabled = true` (default).

### Why Tombstones Must Age

Tombstones must persist long enough to propagate to all replicas via read repair and anti-entropy. Deleting a tombstone too soon risks a "deleted key resurrection" if a stale replica re-propagates the old value. `TombstoneTTL` (7 days) should exceed the maximum expected anti-entropy convergence time.

## Configuration

| Field | Default | Meaning |
|---|---|---|
| `TombstoneGCEnabled` | true | Enable background GC |
| `TombstoneTTLDays` | 7 | Days before tombstone is eligible for GC |
| `TombstoneGCInterval` | 1h | How often GC runs |

TTL sweeper interval is set at construction time in `main.go`.

## Interaction Summary

```
Client writes key with TTL=60s
         │
         ▼
Sibling.ExpiresAt = now + 60
         │
         ▼ (after 60s, lazy)
Storage.Get filters out expired sibling (client sees nothing)
         │
         ▼ (TTL sweeper, every interval)
sweep() detects ExpiresAt <= now
→ Coordinator.Delete → tombstone written to W replicas
         │
         ▼ (after TombstoneTTL = 7d)
tombstoneGCLoop deletes Pebble key entirely
```

Changes:

