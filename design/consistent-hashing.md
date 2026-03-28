---
name: Consistent Hashing
description: Virtual-node hash ring for deterministic key-to-node mapping and preference list generation
type: project
---

# Consistent Hashing

## Overview

The `HashRing` (`internal/cluster/hashring.go`) maps keys to an ordered list of replica nodes (the preference list) using consistent hashing with virtual nodes. Adding or removing a physical node only remaps a small fraction of keys, minimizing rebalancing churn.

## Data Structures

**`VirtualNode`**:
```
Hash     uint64    — xxHash64 of "<nodeID>-<vnodeIdx>"
NodeID   string
VNodeIdx int       — 0 to vnodeCount-1
```

**`HashRing`**:
```
vnodes      []VirtualNode   — sorted ascending by Hash
nodes       map[string]*common.Node
vnodeCount  int             — default 256
mu          sync.RWMutex
```

## Hash Function

All hashing uses **xxHash64** (`github.com/cespare/xxhash/v2`):
- Virtual node positions: `xxhash.Sum64String("<nodeID>-<vnodeIdx>")`
- Key lookup: `xxhash.Sum64(key)`

xxHash64 is chosen for speed (used in the hot path on every read/write).

## Adding and Removing Nodes

`AddNode(node *common.Node)`:
1. Generates `vnodeCount` virtual nodes (default 256) with positions spread around the ring.
2. Inserts them in sorted order via `sort.Slice`.
3. Registers the physical node in `nodes` map.

`RemoveNode(nodeID string)`:
1. Filters `vnodes` slice to remove all entries with matching `NodeID`.
2. Removes from `nodes` map.

Default `vnodeCount = 256` provides good load distribution across nodes. Configurable in `ClusterConfig.VirtualNodeCount` (not yet exposed in YAML).

## Preference List Lookup

`GetPreferenceList(key []byte, n int) ([]*common.Node, error)`:
1. Hashes `key` with xxHash64.
2. Binary searches `vnodes` for the first virtual node with `hash >= keyHash` (clockwise walk from key position).
3. Walks the ring clockwise, collecting **distinct physical nodes** until `n` unique nodes are gathered (or the ring is exhausted).
4. Returns at most `n` nodes.

`GetExtendedPreferenceList(key []byte, n, extra int)` returns `n+extra` nodes, used by sloppy quorum to find overflow nodes beyond the primary N.

`GetPrimaryNode(key []byte)` returns the first node in the preference list — used for single-node operations.

## Other Accessors

| Function | Return |
|---|---|
| `GetNode(nodeID)` | `*common.Node` by ID |
| `Nodes()` | Copy of all physical nodes as `[]*common.Node` |
| `GetAllNodes()` | All node IDs as `[]string` |
| `Size()` | Count of physical nodes |
| `VNodeCount()` | Current `vnodeCount` setting |

## System Flow

```
Put/Get request arrives at coordinator
         │
         ▼
ring.GetPreferenceList(key, N)
         │
         ▼
[node1, node2, node3]  ← preference list (clockwise from key hash)
         │
         ├─ localWrite  (if self in list)
         └─ rpc.RemotePut/RemoteGet  (for peers in list)
```

## Where It Is Used

| Caller | Purpose |
|---|---|
| `Coordinator.Put` | Get N write targets (+ extra for sloppy quorum) |
| `Coordinator.Get` | Get N read targets |
| `Coordinator.Delete` | Get N write targets for tombstone |
| `Coordinator.JoinNode` | Re-add node after join to route correctly |
| `Coordinator.LeaveNode` | Remove node before routing stops |
| `AntiEntropy.exchangeRange` | Determine which peers share a key range |
| `debug.go /debug/ring` | Expose ring topology for diagnostics |

## Rebalancing

When a node joins or leaves, `Coordinator.JoinNode` / `LeaveNode` calls `ring.AddNode` / `ring.RemoveNode`. Only keys that fall between the newly added/removed virtual node positions need rebalancing. The anti-entropy service handles actual data transfer after ring changes.

Changes:

