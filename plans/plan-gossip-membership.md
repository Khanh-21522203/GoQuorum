# Feature: Gossip-Based Membership

## 1. Purpose

GoQuorum currently uses static, configuration-file-based cluster membership: each node knows its peers from a fixed list in `config.yaml`. This is brittle — adding or removing nodes requires restarting the cluster with updated configs. Gossip-based membership replaces static configuration with a self-organizing protocol: nodes exchange membership state periodically with random peers, propagating joins, leaves, and failures throughout the cluster within O(log N) rounds. This feature is not yet implemented.

## 2. Responsibilities

- **Gossip protocol**: Periodically select K random peers and exchange the full membership state (node list with versions); merge incoming state using a last-write-wins strategy per node entry
- **Node join**: A new node contacts one seed node, announces itself, and the gossip protocol disseminates its presence
- **Node leave (graceful)**: A node marks itself as leaving before shutdown, gossips the LEAVING state so peers can drain in-flight requests
- **Failure convergence**: Combine with the failure detector — when a node is marked FAILED, gossip the FAILED state so all nodes stop routing to it within O(log N) rounds
- **Membership store**: Maintain a thread-safe, versioned map of all known nodes (ID → NodeInfo with version counter)
- **Ring integration**: Update the hash ring whenever membership changes (node added, removed, or state changed)

## 3. Non-Responsibilities

- Does not replace the failure detector (heartbeats still detect fast failures; gossip disseminates the state)
- Does not implement consensus or leader election (GoQuorum is leaderless)
- Does not implement node weight or capacity-based routing
- Does not persist membership state to disk — it is rebuilt by gossiping with seeds on restart

## 4. Architecture Design

```
Each node maintains a MembershipTable:
  { nodeID → NodeEntry{ID, Addr, State, Version, UpdatedAt} }

Gossip round (every gossip_interval):
  pick K random ACTIVE peers
  send GossipPush(localTable) to each
  receive GossipPull(peerTable) response
  merge peerTable into localTable (higher version wins per entry)
  update HashRing for any changed entries

Node join:
  new node → seed node: GossipPush({self: JOINING})
  seed merges, gossips to rest of cluster
  new node starts receiving gossip from others

Node failure:
  FailureDetector marks node N as FAILED
  → MembershipStore.MarkFailed(N) → increments N's version with State=FAILED
  → gossip disseminates FAILED state
  → HashRing.UpdateNodeState(N, NodeStateFailed)
```

### Gossip Propagation Speed
With K=2 peers per round and N nodes, a state change reaches all nodes in O(log₂ N) rounds. At `gossip_interval=1s`, a 100-node cluster converges in ~7 rounds (~7 seconds).

## 5. Core Data Structures (Go)

```go
package cluster

import (
    "sync"
    "time"
)

// NodeEntry is one row in the membership table.
type NodeEntry struct {
    ID        string
    Address   string      // gRPC host:port
    State     NodeState   // JOINING, ACTIVE, SUSPECT, FAILED, LEAVING, LEFT
    Version   uint64      // monotonically increasing; higher version wins in merge
    UpdatedAt time.Time
}

// NodeState for gossip includes additional transient states.
type NodeState int

const (
    NodeStateJoining  NodeState = iota
    NodeStateActive
    NodeStateSuspect
    NodeStateFailed
    NodeStateLeaving
    NodeStateLeft
)

// MembershipStore is the thread-safe membership table with gossip merge semantics.
type MembershipStore struct {
    mu      sync.RWMutex
    entries map[string]*NodeEntry  // key = nodeID
    localID string
    onChange []func(entry NodeEntry) // callbacks for ring, failure detector
}

// GossipService manages gossip rounds and handles incoming gossip RPCs.
type GossipService struct {
    config   GossipConfig
    store    *MembershipStore
    rpcPool  *RPCClientPool
    seeds    []string
    stopCh   chan struct{}
    wg       sync.WaitGroup
    logger   *slog.Logger
    metrics  *GossipMetrics
}

// GossipConfig holds all tuning parameters.
type GossipConfig struct {
    Enabled         bool          `yaml:"enabled" default:"true"`
    GossipInterval  time.Duration `yaml:"gossip_interval" default:"1s"`
    FanOut          int           `yaml:"fanout" default:"2"`
    Seeds           []string      `yaml:"seeds"`
    JoinTimeout     time.Duration `yaml:"join_timeout" default:"30s"`
}
```

## 6. Public Interfaces

```go
package cluster

// NewMembershipStore creates an empty membership store with the local node registered as ACTIVE.
func NewMembershipStore(localID, localAddr string) *MembershipStore

// AddNode adds or updates a node entry. Returns true if state changed.
func (s *MembershipStore) AddNode(entry NodeEntry) bool

// MarkFailed marks nodeID as FAILED and increments its version.
func (s *MembershipStore) MarkFailed(nodeID string) bool

// MarkLeft marks nodeID as LEFT (graceful departure).
func (s *MembershipStore) MarkLeft(nodeID string) bool

// ActiveNodes returns all nodes in ACTIVE state.
func (s *MembershipStore) ActiveNodes() []NodeEntry

// AllNodes returns all known nodes regardless of state.
func (s *MembershipStore) AllNodes() []NodeEntry

// Get returns the NodeEntry for nodeID.
func (s *MembershipStore) Get(nodeID string) (NodeEntry, bool)

// Merge applies peerEntries to the local store using last-write-wins (higher version wins).
// Calls onChange callbacks for any entries that changed state or address.
func (s *MembershipStore) Merge(peerEntries []NodeEntry)

// RegisterOnChange registers a callback invoked on membership changes.
// Used to update the hash ring when nodes join/leave/fail.
func (s *MembershipStore) RegisterOnChange(fn func(NodeEntry))

// NewGossipService creates a GossipService.
func NewGossipService(cfg GossipConfig, store *MembershipStore, rpcPool *RPCClientPool, logger *slog.Logger) *GossipService

// Start begins gossiping and initiates the join sequence.
func (gs *GossipService) Start() error

// Stop gracefully marks the local node as LEAVING and stops gossip.
func (gs *GossipService) Stop()

// HandleGossipPush is the gRPC handler for incoming gossip from peers.
// Called by InternalAPI.Gossip().
func (gs *GossipService) HandleGossipPush(peerEntries []NodeEntry) []NodeEntry
```

## 7. Internal Algorithms

### Gossip Round
```
gossipRound():
  peers = store.ActiveNodes()
  if len(peers) == 0: return

  // Select K random peers (excluding self)
  targets = randomSample(peers, min(config.FanOut, len(peers)))

  localState = store.AllNodes()
  for _, target in targets:
    go func():
      ctx, cancel = context.WithTimeout(background, 2s)
      defer cancel()
      peerState, err = rpcPool.GossipExchange(ctx, target.ID, localState)
      if err == nil:
        store.Merge(peerState)
        metrics.GossipRoundsTotal.Inc()
      else:
        metrics.GossipErrors.Inc()
```

### Merge (Last-Write-Wins per node)
```
Merge(peerEntries):
  s.mu.Lock()
  defer s.mu.Unlock()

  for _, incoming in peerEntries:
    existing, ok = s.entries[incoming.ID]
    if !ok || incoming.Version > existing.Version:
      changed = !ok || existing.State != incoming.State || existing.Address != incoming.Address
      s.entries[incoming.ID] = &incoming
      if changed:
        for _, cb in s.onChange:
          go cb(incoming)
```

### Join Sequence
```
Start():
  // Register self as JOINING
  store.AddNode(NodeEntry{
    ID:      localID,
    Address: localAddr,
    State:   NodeStateJoining,
    Version: 1,
  })

  // Contact seed nodes to announce self
  for _, seed in config.Seeds:
    peerState, err = rpcPool.GossipExchange(ctx, seed, store.AllNodes())
    if err == nil:
      store.Merge(peerState)
      break  // one successful seed is enough

  // Mark self as ACTIVE
  store.AddNode(NodeEntry{..., State: NodeStateActive, Version: 2})

  // Start gossip loop
  go gossipLoop()
```

### Graceful Departure
```
Stop():
  // Mark self as LEAVING, gossip it
  store.MarkLeft(localID)
  // Do 3 extra gossip rounds to propagate departure
  for i in 0..3:
    gossipRound()
  stop gossip loop
```

## 8. Persistence Model

Membership state is ephemeral — it is not written to disk. On restart, a node re-discovers the cluster by gossiping with seed nodes. Seeds are the only statically configured addresses; all other nodes are learned dynamically.

The seed list should contain at least 2 stable nodes to tolerate one seed being down during a restart. Seeds do not need to be special — any running node can serve as a seed.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `sync.RWMutex` on MembershipStore | RLock for reads; Lock for Merge and state updates |
| Goroutine per gossip target | Fanout gossip sends are parallel |
| `onChange` callbacks in goroutines | State-change callbacks dispatched asynchronously to avoid holding the mutex |
| Single gossip loop goroutine | Serializes round timing; individual sends are parallel within each round |

## 10. Configuration

```go
type GossipConfig struct {
    Enabled        bool          `yaml:"enabled" default:"true"`
    GossipInterval time.Duration `yaml:"gossip_interval" default:"1s"`
    FanOut         int           `yaml:"fanout" default:"2"`
    Seeds          []string      `yaml:"seeds"`
    JoinTimeout    time.Duration `yaml:"join_timeout" default:"30s"`
}
```

## 11. Observability

- `goquorum_gossip_rounds_total` — Counter of completed gossip exchanges
- `goquorum_gossip_errors_total` — Counter of failed gossip exchanges
- `goquorum_membership_node_count{state}` — Gauge of known nodes per state
- `goquorum_gossip_merge_updates_total` — Counter of membership entries updated per merge
- Log at INFO when a new node joins (first time seen as ACTIVE)
- Log at WARN when a node transitions to FAILED or LEFT
- Admin `/debug/cluster` endpoint returns full membership table with versions

## 12. Testing Strategy

- **Unit tests**:
  - `TestMergeHigherVersionWins`: incoming entry with higher version replaces local
  - `TestMergeLowerVersionIgnored`: incoming entry with lower version does not overwrite
  - `TestMergeNewNodeAdded`: incoming entry for unknown node, assert added
  - `TestOnChangeCallbackFired`: merge causes state change, assert callback called
  - `TestMarkFailed`: MarkFailed increments version and sets state
  - `TestActiveNodesFilters`: mixed states, assert only ACTIVE returned
  - `TestGossipRoundSelectsKPeers`: assert exactly FanOut peers contacted per round
  - `TestJoinSequence`: mock seeds, assert local state merged and self marked ACTIVE
- **Integration tests**:
  - `TestGossipConvergence3Nodes`: 3 nodes, start one at a time, assert all know each other within 5s
  - `TestGossipFailurePropagation`: kill node, assert failure propagates to all others within 10s
  - `TestGossipGracefulLeave`: graceful stop, assert other nodes see LEFT state

## 13. Open Questions

- Should gossip messages be compressed (e.g., snappy) for large clusters (100+ nodes)?
- Should we use delta gossip (only changed entries) instead of full-state exchange for efficiency at large scale?
