# Feature: Consistent Hashing & Hash Ring

## 1. Purpose

The hash ring is GoQuorum's partition and replica placement subsystem. It maps keys to responsible nodes using consistent hashing with virtual nodes, ensuring that adding or removing a cluster member only rebalances a minimal fraction of keys. For every key, the ring computes an ordered preference list of N nodes — the primary and its replicas — that the coordinator uses to fan out reads and writes.

## 2. Responsibilities

- **Ring construction**: Build an ordered ring of virtual node tokens from physical node addresses, using xxHash64
- **Key mapping**: Hash any key to a ring position and walk clockwise to find the first N distinct physical nodes
- **Preference list**: Return an ordered list of N node addresses responsible for a given key
- **Node management**: Add or remove nodes from the ring dynamically, redistributing only the affected tokens
- **Virtual nodes**: Distribute each physical node across the ring using configurable virtual node count (default 256) for uniform load
- **Membership awareness**: Filter the preference list to exclude nodes marked as FAILED by the failure detector

## 3. Non-Responsibilities

- Does not perform any I/O or RPC calls
- Does not track node health (health state is passed in from `membership`)
- Does not store any key-value data
- Does not implement replication — it only determines which nodes should replicate

## 4. Architecture Design

```
Physical Nodes: [node-A, node-B, node-C]
Virtual Nodes per physical: 256

Ring (sorted by token):
  0x0001 → node-B-vnode-0
  0x0002 → node-A-vnode-3
  ...
  0xffff → node-C-vnode-77

Key "user:1234":
  hash = xxHash64("user:1234") = 0x7ab2
  Walk ring clockwise from 0x7ab2
  PreferenceList(N=3) = [node-C, node-A, node-B]
```

```
+--------------------------------------------------+
|                    HashRing                       |
|                                                   |
|  tokens: []TokenEntry  (sorted ascending)         |
|  nodes: map[NodeID]NodeInfo                       |
|  virtualNodes: int (default 256)                  |
|                                                   |
|  AddNode(id, addr)                                |
|  RemoveNode(id)                                   |
|  PreferenceList(key, n) []NodeInfo                |
|  ResponsibleNode(key) NodeInfo                    |
|                                                   |
+--------------------------------------------------+
```

### Token Distribution

Each physical node contributes `virtualNodes` tokens to the ring. Token for virtual node `i` of node with ID `nodeID` is:

```
token = xxHash64(nodeID + "#" + strconv.Itoa(i))
```

## 5. Core Data Structures (Go)

```go
package cluster

import (
    "sort"
    "sync"
    "github.com/cespare/xxhash/v2"
)

// NodeID is the unique string identifier for a cluster node.
type NodeID = string

// NodeInfo holds the contact details and state of a cluster member.
type NodeInfo struct {
    ID      NodeID
    Address string // host:port for gRPC
    State   NodeState
}

// NodeState represents the liveness of a node as seen by the failure detector.
type NodeState int

const (
    NodeStateActive  NodeState = iota
    NodeStateSuspect           // missed some heartbeats
    NodeStateFailed            // presumed dead
)

// TokenEntry is one slot on the consistent hash ring.
type TokenEntry struct {
    Token  uint64 // ring position
    NodeID NodeID
}

// HashRing is a thread-safe consistent hash ring with virtual nodes.
type HashRing struct {
    mu           sync.RWMutex
    tokens       []TokenEntry // sorted ascending by Token
    nodes        map[NodeID]NodeInfo
    virtualNodes int
}
```

## 6. Public Interfaces

```go
package cluster

// NewHashRing creates an empty ring with the given virtual node count.
func NewHashRing(virtualNodes int) *HashRing

// AddNode inserts a node and its virtual tokens into the ring.
// Safe to call on an already-added node (idempotent update).
func (r *HashRing) AddNode(info NodeInfo)

// RemoveNode removes a node and all its virtual tokens from the ring.
func (r *HashRing) RemoveNode(nodeID NodeID)

// PreferenceList returns the ordered list of up to n distinct physical nodes
// responsible for key, starting from key's hash position and walking clockwise.
// Nodes in NodeStateFailed are skipped unless no live nodes remain.
func (r *HashRing) PreferenceList(key []byte, n int) []NodeInfo

// ResponsibleNode returns the primary (first) node for a key.
func (r *HashRing) ResponsibleNode(key []byte) NodeInfo

// NodeCount returns the number of physical nodes in the ring.
func (r *HashRing) NodeCount() int

// UpdateNodeState updates the NodeState for a node already in the ring.
func (r *HashRing) UpdateNodeState(nodeID NodeID, state NodeState)

// Nodes returns all current NodeInfo entries (snapshot).
func (r *HashRing) Nodes() []NodeInfo
```

## 7. Internal Algorithms

### AddNode
```
AddNode(info):
  r.mu.Lock()
  defer r.mu.Unlock()

  r.nodes[info.ID] = info

  // Remove existing tokens for this node (in case of re-add)
  r.tokens = filter(r.tokens, token.NodeID != info.ID)

  // Add virtualNodes new tokens
  for i in 0..virtualNodes:
    hash = xxHash64(info.ID + "#" + i)
    r.tokens = append(r.tokens, TokenEntry{hash, info.ID})

  sort.Slice(r.tokens, by Token ascending)
```

### PreferenceList
```
PreferenceList(key, n):
  r.mu.RLock()
  defer r.mu.RUnlock()

  if len(r.tokens) == 0: return []

  keyHash = xxHash64(key)

  // Binary search for first token >= keyHash
  idx = sort.Search(len(r.tokens), r.tokens[i].Token >= keyHash)
  idx = idx % len(r.tokens)  // wrap around

  seen = map[NodeID]bool{}
  result = []NodeInfo{}

  for len(result) < n && len(seen) < len(r.nodes):
    entry = r.tokens[idx % len(r.tokens)]
    node = r.nodes[entry.NodeID]

    if !seen[entry.NodeID]:
      seen[entry.NodeID] = true
      if node.State != NodeStateFailed:
        result = append(result, node)

    idx++

  // Fallback: include failed nodes if we couldn't fill n
  if len(result) < n:
    for nodeID, node in r.nodes:
      if !seen[nodeID]:
        result = append(result, node)
        if len(result) == n: break

  return result
```

### RemoveNode
```
RemoveNode(nodeID):
  r.mu.Lock()
  defer r.mu.Unlock()

  delete(r.nodes, nodeID)
  r.tokens = filter(r.tokens, token.NodeID != nodeID)
  // tokens remain sorted (filter preserves order)
```

## 8. Persistence Model

The hash ring is an in-memory data structure. It is rebuilt at startup from the cluster membership configuration (static seed nodes) or from the gossip membership state. There is no separate persistence file for the ring itself.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `sync.RWMutex` | RLock for `PreferenceList` and `ResponsibleNode` (hot path); Lock for `AddNode`, `RemoveNode`, `UpdateNodeState` |
| Immutable token slice snapshot | All callers hold RLock only during the walk; no pointer aliasing issues |

`PreferenceList` is called on every client request. The RLock is held only for the duration of the ring walk (O(log n) binary search + O(N) walk), making lock contention negligible.

## 10. Configuration

```go
type HashRingConfig struct {
    // VirtualNodes is the number of virtual tokens per physical node.
    VirtualNodes int `yaml:"virtual_nodes" default:"256"`
}
```

Increasing virtual nodes improves load balance at the cost of higher memory usage (each token is 16 bytes; 256 * 100 nodes = ~400KB).

## 11. Observability

- `goquorum_ring_node_count` — Gauge of number of physical nodes in ring
- `goquorum_ring_virtual_token_count` — Gauge of total virtual tokens in ring
- Log at INFO when a node is added or removed from the ring (with node ID and address)
- `PreferenceList` result logged at DEBUG per request (useful for tracing replication paths)
- Admin `/debug/ring` endpoint exposes the full token → node mapping as JSON

## 12. Testing Strategy

- **Unit tests**:
  - `TestAddAndPreferenceList`: add 3 nodes, assert PreferenceList(key, 3) returns all 3 distinct nodes
  - `TestPreferenceListOrdering`: assert ring walk is clockwise and wraps around correctly
  - `TestRemoveNode`: add 3 nodes, remove 1, assert PreferenceList returns only 2 nodes
  - `TestVirtualNodeUniformity`: add 3 nodes with 256 vnodes each, sample 10,000 keys, assert each node handles 30%±5% of keys
  - `TestConsistency`: add 3 nodes, record assignments for 1,000 keys; add a 4th node; assert <25% of keys reassigned
  - `TestFailedNodeSkipped`: mark one node failed, assert PreferenceList of N=2 skips it
  - `TestFailedNodeFallback`: all nodes failed except 1, assert PreferenceList(N=3) returns the surviving node
  - `TestConcurrentAccess`: run AddNode and PreferenceList from multiple goroutines, assert no race (run with -race)
- **Integration tests**:
  - `TestRingRebalanceLive3Nodes`: in-process 3-node cluster, add a 4th node, verify keys still reachable

## 13. Open Questions

None.
