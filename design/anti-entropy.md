---
name: Anti-Entropy
description: Background Merkle tree-based synchronization that reconciles divergent replicas across all keys
type: project
---

# Anti-Entropy

## Overview

`AntiEntropy` (`internal/cluster/anti_entropy.go`) runs a background loop that periodically compares each node's Merkle tree with its peers. When trees diverge, it pushes the differing keys to the peer. This catches divergence that read repair misses (cold keys, post-partition recovery, tombstone propagation).

## MerkleTree (`internal/cluster/merkle_tree.go`)

A binary Merkle tree over key buckets used to efficiently detect which portions of the keyspace differ between nodes.

### Structure

```
depth       int         — default 10; produces 2^10 = 1024 leaf buckets
numBuckets  int         — 2^depth
leafHashes  [][32]byte  — SHA256 hash per bucket (2^depth entries)
nodeHashes  [][32]byte  — internal node hashes (2^depth - 1 entries)
dirty       []bool      — flags marking changed buckets (lazy rebuild)
mu          sync.RWMutex
```

### Key-to-Bucket Mapping

`keyToBucket(key string) int`:
1. SHA256(key) → 32 bytes.
2. BigEndian uint64 of first 8 bytes.
3. `% numBuckets` → bucket index 0..1023.

### Incremental Update (XOR Toggle)

`UpdateKey(key string, siblings SiblingSet)`:
- Computes `h = SHA256(key + encode(siblings))`.
- XORs `h` into `leafHashes[bucket]` (XOR is self-inverse: applying twice cancels).
- Sets `dirty[bucket] = true`.

`RemoveKey(key string, oldSiblings SiblingSet)`:
- Same XOR operation with the old hash — undoes the contribution of the deleted key.

This means: if a key is added then removed, the leaf hash returns to its prior value. O(1) per update.

### Lazy Rebuild

`GetRoot()` calls `rebuildIfNeeded()`:
- If any `dirty[i]` is true: `rebuildTree()` (bottom-up, O(numBuckets)).
- Returns `nodeHashes[0]` (root = first internal node).

`rebuildTree()`: For each level from `depth-1` down to 0, each internal node hash = `SHA256(leftChildHash || rightChildHash)`.

### Tree Comparison (`Compare`)

`Compare(other *MerkleTree) []BucketRange`:
1. If depths differ → return `[{0, numBuckets}]` (full divergence).
2. If roots equal → return `nil` (identical).
3. `findDifferences(other, level=0, start=0, end=numBuckets)` recursively:
   - At leaf level (`level == depth`): return `[{start, end}]`.
   - At internal level: compare left and right subtree hashes.
   - Recurse only into differing subtrees → O(log N) comparisons for localized divergence.
4. Returns slice of `BucketRange{Start, End}` covering differing leaf buckets.

## AntiEntropy Service

### Struct

```
nodeID      string
storage     Storage
ring        *HashRing
rpc         RPCClient
merkleTree  *MerkleTree
config      AntiEntropyConfig
stopCh      chan struct{}
wg          sync.WaitGroup
```

### Startup (`Start`)

1. Calls `merkleTree.Build(storage)` — full scan + Merkle tree construction from current storage state.
2. Starts `schedulerLoop()`.

### Scheduler Loop

`schedulerLoop()`:
- Tick interval = `ScanInterval / NumBuckets` (default: 1h / 1024 ≈ 3.5s per bucket).
- Each tick calls `exchangeRange(rangeIdx)` for the next bucket in round-robin order.
- Effectively spreads one full scan over `ScanInterval` (1h).

### Exchange With a Peer (`exchangeWithPeer(peerID, rangeIdx)`)

```
rpc.GetMerkleRoot(peerID) → peerRoot
If peerRoot == localRoot for this range → skip (no divergence)

storage.Scan(bucketRange) → all keys in range
For each key in range:
  rpc.RemotePut(peerID, key, siblings) → push to peer
metrics.KeysExchanged += n
```

Bandwidth is bounded by `MaxBandwidth` (default 10 MB/s) — enforced at the scan level.

### Post-Recovery Sync

`TriggerWithPeer(nodeID string)`: Spawns a goroutine to immediately exchange all buckets with the recovered node. Called by `FailureDetector.OnNodeRecovery`.

`SyncWithPeers(peers []string)`: Synchronous full drain to multiple peers — used during graceful node leave to ensure data is transferred before departure.

### Incremental Updates from Coordinator

`OnKeyUpdate(key string, siblings SiblingSet)`: called by `Coordinator.localWrite` after every write → `merkleTree.UpdateKey`.

`OnKeyDelete(key string, oldSiblings SiblingSet)`: called by `Coordinator.Delete` → `merkleTree.RemoveKey`.

These keep the in-memory Merkle tree in sync without a full rescan.

## Configuration (`internal/config/repair.go`)

| Field | Default | Description |
|---|---|---|
| `Enabled` | true | Enable anti-entropy |
| `ScanInterval` | 1h | Full keyspace scan period |
| `ExchangeTimeout` | 30s | Timeout for per-peer exchange |
| `MaxBandwidth` | 10 MB/s | Data push rate limit |
| `Parallelism` | 1 | Concurrent exchange goroutines |
| `MerkleDepth` | 10 | Tree depth (1024 buckets) |

`NumBuckets() = 2^MerkleDepth`. Increase depth for larger datasets (more granular divergence detection; more memory).

## Metrics (`internal/cluster/anti_entropy_metrics.go`)

| Metric | Type | Description |
|---|---|---|
| `ExchangesTotal` | Counter | Exchange attempts |
| `ExchangesFailed` | Counter | Failed exchanges |
| `KeysExchanged` | Counter | Keys pushed to peers |
| `BytesSent` / `BytesReceived` | Counter | Data volume |
| `ScanDuration` | Histogram (exp 1s–2^14s) | Full scan time |
| `MerkleTreeDiffNodes` | Histogram (linear 0–200) | Divergent nodes per exchange |

## System Flow

```
Background scheduler tick
       │
       ▼
exchangeRange(bucketIdx)
       │
       ├─ for each peer in ring.GetPreferenceList(bucketKey, N):
       │       exchangeWithPeer(peer, bucketIdx)
       │             │
       │             ├─ rpc.GetMerkleRoot → compare
       │             └─ if divergent: scan + push keys
       │
       └─ advance bucketIdx (round-robin)

Write path: Coordinator → localWrite → antiEntropy.OnKeyUpdate → leafHash XOR
```

Changes:

