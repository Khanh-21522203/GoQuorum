# Feature: Anti-Entropy with Merkle Trees

## 1. Purpose

Anti-entropy is the proactive background consistency mechanism that detects and repairs divergence between nodes without requiring a client read. It uses a Merkle tree over the key space to efficiently identify which key ranges differ between two nodes, then exchanges only the divergent keys. Anti-entropy complements read repair by healing keys that are rarely or never read, and it recovers nodes that were offline for extended periods (beyond what hinted handoff covers).

## 2. Responsibilities

- **Merkle tree maintenance**: Build and incrementally update a Merkle tree over all locally stored keys, grouping keys into 2^depth leaf buckets
- **Periodic sync scheduling**: Select a random peer each interval, run the three-phase exchange protocol to identify and repair divergent key ranges
- **Three-phase exchange protocol**:
  1. Compare Merkle roots with the peer; if equal, stop early
  2. Traverse the tree level by level to pinpoint divergent leaf ranges
  3. Exchange all keys in divergent leaf ranges, merging sibling sets
- **Bandwidth limiting**: Cap the number of keys exchanged per round to bound network load
- **Key space partitioning**: Divide the key space into configurable range slices; process one range per interval cycle
- **Incremental tree updates**: Update leaf hash when a key is written or deleted, without full tree rebuild

## 3. Non-Responsibilities

- Does not repair all key space in a single round (incremental by design)
- Does not implement the actual key-value merge logic (delegated to `storage.Engine.Merge`)
- Does not implement the RPC transport (delegated to `rpcPool`)
- Does not replace read repair for hot keys — anti-entropy handles cold/offline divergence

## 4. Architecture Design

```
AntiEntropy background goroutine:
  tick every scan_interval
    |
    select random peer from membership
    |
    Phase 1: Exchange Merkle roots
      [local root] <-- GetMerkleRoot RPC --> [peer root]
      if equal: done (no divergence)
    |
    Phase 2: Tree traversal (BFS)
      exchange child hashes level by level
      identify leaf buckets with hash mismatch
    |
    Phase 3: Key exchange
      for each divergent leaf bucket:
        exchange all keys in bucket range
        merge remote SiblingSet into local storage
        send local SiblingSet to peer if peer is stale

Merkle Tree Structure:
  depth=10 → 2^10=1024 leaf buckets
  each leaf covers a contiguous range of xxHash64 values

  Root (hash of all children)
  ├── L0: hash(L0.children)
  │   ├── L00: hash(leaf0 keys)
  │   └── L01: hash(leaf1 keys)
  └── L1: ...
         └── Leaf[1023]: hash(keys in bucket 1023)
```

## 5. Core Data Structures (Go)

```go
package cluster

import (
    "crypto/sha256"
    "sync"
    "time"
    "goquorum/internal/storage"
)

// MerkleTree is an in-memory Merkle tree over the local key space.
// Leaf nodes correspond to key ranges; internal nodes hash their children.
type MerkleTree struct {
    mu     sync.RWMutex
    depth  int        // tree depth; leaves = 2^depth
    leaves [][]byte   // SHA256 hash per leaf bucket; len = 2^depth
    nodes  [][]byte   // internal node hashes; len = 2^(depth+1) - 1
}

// AntiEntropy orchestrates periodic peer synchronization.
type AntiEntropy struct {
    config    AntiEntropyConfig
    localID   string
    local     storage.Engine
    tree      *MerkleTree
    members   *Membership
    rpcPool   *RPCClientPool

    stopCh  chan struct{}
    wg      sync.WaitGroup

    logger  *slog.Logger
    metrics *AntiEntropyMetrics
}

// AntiEntropyConfig holds all tuning parameters.
type AntiEntropyConfig struct {
    Enabled          bool          `yaml:"enabled" default:"true"`
    ScanInterval     time.Duration `yaml:"scan_interval" default:"1h"`
    MerkleDepth      int           `yaml:"merkle_depth" default:"10"`
    MaxKeysPerRound  int           `yaml:"max_keys_per_round" default:"10000"`
    ExchangeTimeout  time.Duration `yaml:"exchange_timeout" default:"30s"`
    Parallelism      int           `yaml:"parallelism" default:"4"`
}

// KeyEntry is one key-value pair exchanged during anti-entropy.
type KeyEntry struct {
    Key    []byte
    SibSet *storage.SiblingSet
}
```

## 6. Public Interfaces

```go
package cluster

// NewMerkleTree builds a Merkle tree of the given depth.
// depth=10 creates 1024 leaf buckets.
func NewMerkleTree(depth int) *MerkleTree

// UpdateKey recomputes the leaf hash for the bucket containing key,
// then propagates the change up to the root.
// Called by storage.Engine on every Put/Delete.
func (t *MerkleTree) UpdateKey(key []byte, sibSet *storage.SiblingSet)

// Root returns the current root hash of the tree (SHA256, 32 bytes).
func (t *MerkleTree) Root() []byte

// LeafHash returns the hash of leaf bucket idx.
func (t *MerkleTree) LeafHash(idx int) []byte

// ChildHashes returns the hashes of both children of internal node idx.
func (t *MerkleTree) ChildHashes(nodeIdx int) (left, right []byte)

// LeafBucket returns the leaf index for a given key.
func (t *MerkleTree) LeafBucket(key []byte) int

// KeysInBucket returns all keys stored in the given leaf bucket.
// Requires a scan of local storage.
func (t *MerkleTree) KeysInBucket(idx int, eng storage.Engine) ([][]byte, error)

// NewAntiEntropy creates an AntiEntropy instance.
func NewAntiEntropy(
    cfg AntiEntropyConfig,
    localID string,
    local storage.Engine,
    tree *MerkleTree,
    members *Membership,
    rpcPool *RPCClientPool,
    logger *slog.Logger,
) *AntiEntropy

// Start launches the background sync goroutine.
func (ae *AntiEntropy) Start()

// Stop gracefully shuts down anti-entropy.
func (ae *AntiEntropy) Stop()

// RunSyncWithPeer executes one full sync round with the specified peer.
// Exported for testing and manual admin triggering.
func (ae *AntiEntropy) RunSyncWithPeer(ctx context.Context, peerID string) error
```

## 7. Internal Algorithms

### Background Sync Loop
```
syncLoop():
  ticker = time.NewTicker(ae.config.ScanInterval)
  for:
    select:
    case <-ticker.C:
      peers = ae.members.ActivePeers()
      if len(peers) == 0: continue
      peer = randomChoice(peers)
      ctx, cancel = context.WithTimeout(background, ae.config.ExchangeTimeout)
      ae.RunSyncWithPeer(ctx, peer.ID)
      cancel()
    case <-ae.stopCh:
      return
```

### RunSyncWithPeer (Three-Phase Protocol)
```
RunSyncWithPeer(ctx, peerID):
  // Phase 1: Root comparison
  peerRoot = rpcPool.GetMerkleRoot(ctx, peerID)
  localRoot = ae.tree.Root()
  if bytes.Equal(peerRoot, localRoot):
    ae.metrics.NoopTotal.Inc()
    return nil  // no divergence

  // Phase 2: BFS tree traversal to find divergent leaves
  divergentLeaves = []int{}
  queue = [0]  // start at root node index 0

  while len(queue) > 0:
    nodeIdx = dequeue(queue)
    localLeft, localRight = ae.tree.ChildHashes(nodeIdx)
    peerLeft, peerRight = rpcPool.GetChildHashes(ctx, peerID, nodeIdx)

    leftLeaf, rightLeaf = isLeafLevel(nodeIdx, ae.config.MerkleDepth)

    if !bytes.Equal(localLeft, peerLeft):
      if leftLeaf: divergentLeaves = append(divergentLeaves, leftChildLeafIdx(nodeIdx))
      else:        queue = append(queue, leftChildNodeIdx(nodeIdx))

    if !bytes.Equal(localRight, peerRight):
      if rightLeaf: divergentLeaves = append(divergentLeaves, rightChildLeafIdx(nodeIdx))
      else:         queue = append(queue, rightChildNodeIdx(nodeIdx))

  // Phase 3: Key exchange for each divergent leaf
  keysExchanged = 0
  sem = make(chan struct{}, ae.config.Parallelism)

  for _, leafIdx := range divergentLeaves:
    if keysExchanged >= ae.config.MaxKeysPerRound: break
    sem <- struct{}{}
    go func(idx int):
      defer func() { <-sem }()
      ae.exchangeLeaf(ctx, peerID, idx)
      keysExchanged++
    (leafIdx)

  drain(sem)
  return nil
```

### exchangeLeaf
```
exchangeLeaf(ctx, peerID, leafIdx):
  // Get local keys in this bucket
  localKeys = ae.tree.KeysInBucket(leafIdx, ae.local)
  localEntries = []KeyEntry{}
  for each key in localKeys:
    sibSet = ae.local.Get(key)
    localEntries = append(localEntries, KeyEntry{key, sibSet})

  // Exchange with peer
  peerEntries = rpcPool.AntiEntropyExchange(ctx, peerID, leafIdx, localEntries)

  // Merge peer's data into local storage
  for each entry in peerEntries:
    ae.local.Merge(entry.Key, entry.SibSet)
    ae.tree.UpdateKey(entry.Key, merged)
```

### MerkleTree.UpdateKey
```
UpdateKey(key, sibSet):
  t.mu.Lock()
  defer t.mu.Unlock()

  bucketIdx = xxHash64(key) % (1 << t.depth)
  leafHash = computeLeafHash(t.local, bucketIdx)  // SHA256 of sorted key:sibSet encodings
  t.leaves[bucketIdx] = leafHash

  // Propagate up: update parent hashes from leaf to root
  nodeIdx = leafToNodeIdx(bucketIdx)
  while nodeIdx >= 0:
    left = t.nodes[leftChild(nodeIdx)]
    right = t.nodes[rightChild(nodeIdx)]
    t.nodes[nodeIdx] = sha256(left || right)
    nodeIdx = parent(nodeIdx)
```

## 8. Persistence Model

The Merkle tree is rebuilt in-memory at startup by scanning all keys in local storage. This is an O(N) operation at startup time. No separate tree persistence file is maintained to avoid consistency issues between the tree and the storage state.

Key exchange data (sibling sets) flows through the `AntiEntropyExchange` gRPC RPC and is merged into Pebble via the standard `storage.Engine.Merge` path.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `sync.RWMutex` on MerkleTree | RLock for root/hash reads; Lock for UpdateKey |
| `sem` channel (bounded parallelism) | Limits concurrent leaf exchanges per sync round |
| Single background goroutine | One sync round at a time; no concurrent rounds with same peer |
| `context.WithTimeout` | Bounds each sync round duration |

## 10. Configuration

```go
type AntiEntropyConfig struct {
    Enabled         bool          `yaml:"enabled" default:"true"`
    ScanInterval    time.Duration `yaml:"scan_interval" default:"1h"`
    MerkleDepth     int           `yaml:"merkle_depth" default:"10"`
    MaxKeysPerRound int           `yaml:"max_keys_per_round" default:"10000"`
    ExchangeTimeout time.Duration `yaml:"exchange_timeout" default:"30s"`
    Parallelism     int           `yaml:"parallelism" default:"4"`
}
```

## 11. Observability

- `goquorum_anti_entropy_sync_total{result="success|error|noop"}` — Per-round sync outcomes
- `goquorum_anti_entropy_divergent_leaves_total` — Total divergent leaf buckets found per round
- `goquorum_anti_entropy_keys_exchanged_total` — Keys sent/received per sync round
- `goquorum_anti_entropy_sync_duration_seconds` — Histogram of sync round duration
- `goquorum_anti_entropy_tree_update_duration_seconds` — Time to update tree hash on write
- `goquorum_merkle_tree_root_hash` — Current root hash (as label or log), for admin visibility
- Log at INFO at start and end of each sync round, including peer, divergent leaves, keys exchanged
- Log at WARN if sync round is truncated due to `MaxKeysPerRound`

## 12. Testing Strategy

- **Unit tests**:
  - `TestMerkleTreeRoot`: empty tree, insert one key, assert root changes
  - `TestMerkleTreeLeafBucket`: verify key maps to expected bucket index
  - `TestMerkleTreeUpdatePropagates`: update a leaf, assert all ancestor hashes change
  - `TestMerkleTreeNoChangeOnSameValue`: update with same sibling set, assert root unchanged
  - `TestPhase1EarlyExit`: identical roots, assert no keys exchanged
  - `TestPhase2FindsDivergentLeaf`: two trees differing in one leaf, assert only that leaf exchanged
  - `TestExchangeLeafMergesData`: peer has newer version, assert local storage updated
  - `TestMaxKeysPerRoundLimit`: many divergent keys, assert exchange stops at limit
  - `TestParallelLeafExchange`: assert `Parallelism` goroutines used concurrently
- **Integration tests**:
  - `TestAntiEntropyConverges3Nodes`: write to node A, A goes offline, write same key to B, A reconnects, assert anti-entropy syncs A
  - `TestAntiEntropyRebuildFromScratch`: stop node, delete 20% of keys from storage, restart, assert anti-entropy restores them

## 13. Open Questions

None.
