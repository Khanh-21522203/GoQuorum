# Feature: Failure Detection

## 1. Purpose

The failure detector monitors the health of all peer nodes in the cluster using a heartbeat protocol. Each node periodically sends a heartbeat to all peers; a peer that misses too many consecutive heartbeats is transitioned through ACTIVE → SUSPECT → FAILED states. The coordinator and hash ring use this state to avoid routing requests to dead nodes, improving availability and reducing latency from futile RPCs.

## 2. Responsibilities

- **Heartbeat emission**: Periodically send a `Heartbeat` RPC to every known peer, including the sender's current node status
- **Heartbeat reception**: Handle incoming heartbeats from peers, resetting their missed-heartbeat counter
- **State machine**: Track each peer's state (UNKNOWN → ACTIVE → SUSPECT → FAILED); advance based on missed heartbeat count
- **Membership integration**: Notify `Membership` when a peer's state changes so the hash ring and coordinator can update routing
- **Self-reporting**: Include the local node's load and status information in outgoing heartbeats for cluster-wide visibility

## 3. Non-Responsibilities

- Does not implement the `Membership` store or hash ring (it notifies them)
- Does not evict failed nodes from cluster membership permanently (nodes can rejoin)
- Does not implement network-level timeouts (delegated to gRPC context deadlines)
- Does not implement gossip-based membership dissemination (see `plan-gossip-membership.md`)

## 4. Architecture Design

```
FailureDetector background loop:
  every heartbeat_interval:
    for each peer in membership.Peers():
      go sendHeartbeat(peer)

  sendHeartbeat(peer):
    resp, err = rpcPool.Heartbeat(ctx, peer, localStatus)
    if err:
      peer.missedHeartbeats++
      updateState(peer)
    else:
      peer.missedHeartbeats = 0
      peer.state = ACTIVE

  updateState(peer):
    if peer.missedHeartbeats >= failure_threshold:
      if peer.state != FAILED:
        peer.state = FAILED
        notify membership
    elif peer.missedHeartbeats >= suspect_threshold:
      if peer.state == ACTIVE:
        peer.state = SUSPECT
        notify membership
```

### Node State Machine
```
         join/heartbeat received
              |
              v
           [ACTIVE]
              |   \
     missed >= S    missed >= F
         |               |
         v               v
      [SUSPECT]       [FAILED]
         |               |
  heartbeat recv    heartbeat recv
         |               |
         +-------+-------+
                 |
                 v
              [ACTIVE]
```
S = `suspect_threshold`, F = `failure_threshold`

## 5. Core Data Structures (Go)

```go
package cluster

import (
    "sync"
    "time"
)

// NodeState is the liveness classification of a peer node.
type NodeState int

const (
    NodeStateUnknown NodeState = iota
    NodeStateActive
    NodeStateSuspect
    NodeStateFailed
)

// PeerHealth tracks the heartbeat state of one peer node.
type PeerHealth struct {
    NodeID           string
    Address          string
    State            NodeState
    MissedHeartbeats int
    LastHeartbeat    time.Time
    LastStatus       NodeStatus // most recent status received in heartbeat
}

// NodeStatus is included in heartbeats to share basic load/state info.
type NodeStatus struct {
    NodeID      string
    KeyCount    int64
    DiskUsage   int64
    CPULoad     float64
    Uptime      time.Duration
}

// FailureDetector monitors peer health via heartbeats.
type FailureDetector struct {
    config    FailureDetectorConfig
    localID   string
    localAddr string
    peers     map[string]*PeerHealth // guarded by mu
    mu        sync.RWMutex
    rpcPool   *RPCClientPool
    onChange  func(nodeID string, newState NodeState) // membership callback

    stopCh chan struct{}
    wg     sync.WaitGroup

    logger  *slog.Logger
    metrics *FailureDetectorMetrics
}
```

## 6. Public Interfaces

```go
package cluster

// NewFailureDetector creates a FailureDetector.
// onChange is called whenever a peer's NodeState transitions.
func NewFailureDetector(
    cfg FailureDetectorConfig,
    localID, localAddr string,
    rpcPool *RPCClientPool,
    onChange func(nodeID string, newState NodeState),
    logger *slog.Logger,
) *FailureDetector

// Start launches the background heartbeat loop.
func (fd *FailureDetector) Start()

// Stop shuts down the heartbeat loop.
func (fd *FailureDetector) Stop()

// AddPeer registers a peer for monitoring.
func (fd *FailureDetector) AddPeer(nodeID, address string)

// RemovePeer deregisters a peer.
func (fd *FailureDetector) RemovePeer(nodeID string)

// RecordHeartbeat processes a received heartbeat from peerID.
// Called by the gRPC server's Heartbeat handler.
func (fd *FailureDetector) RecordHeartbeat(peerID string, status NodeStatus)

// PeerState returns the current NodeState of peerID.
func (fd *FailureDetector) PeerState(peerID string) NodeState

// ActivePeers returns all peers currently in NodeStateActive.
func (fd *FailureDetector) ActivePeers() []PeerHealth

// AllPeers returns all known peers and their states.
func (fd *FailureDetector) AllPeers() []PeerHealth
```

## 7. Internal Algorithms

### Heartbeat Loop
```
heartbeatLoop():
  ticker = time.NewTicker(fd.config.HeartbeatInterval)
  for:
    select:
    case <-ticker.C:
      peers = fd.AllPeers()
      for each peer in peers:
        go fd.sendHeartbeatTo(peer)
    case <-fd.stopCh:
      return
```

### sendHeartbeatTo
```
sendHeartbeatTo(peer):
  ctx, cancel = context.WithTimeout(background, fd.config.HeartbeatTimeout)
  defer cancel()

  status = fd.buildLocalStatus()
  _, err = fd.rpcPool.Heartbeat(ctx, peer.NodeID, status)

  fd.mu.Lock()
  defer fd.mu.Unlock()

  p = fd.peers[peer.NodeID]
  if p == nil: return  // peer removed concurrently

  if err != nil:
    p.MissedHeartbeats++
    fd.metrics.HeartbeatFailure.WithLabelValues(peer.NodeID).Inc()
  else:
    p.MissedHeartbeats = 0
    p.LastHeartbeat = now()
    p.State = NodeStateActive

  fd.updateState(p)
```

### updateState (State Machine Transitions)
```
updateState(p):
  prevState = p.State
  newState = prevState

  if p.MissedHeartbeats == 0:
    newState = NodeStateActive
  elif p.MissedHeartbeats >= fd.config.FailureThreshold:
    newState = NodeStateFailed
  elif p.MissedHeartbeats >= fd.config.SuspectThreshold:
    newState = NodeStateSuspect

  if newState != prevState:
    p.State = newState
    fd.metrics.StateTransition.WithLabelValues(peer.NodeID, stateName(newState)).Inc()
    log.Info("peer state changed", node=p.NodeID, from=prevState, to=newState)
    go fd.onChange(p.NodeID, newState)  // async to avoid holding mu during callback
```

### RecordHeartbeat (incoming)
```
RecordHeartbeat(peerID, status):
  fd.mu.Lock()
  defer fd.mu.Unlock()

  p = fd.peers[peerID]
  if p == nil:
    // New peer — auto-register
    fd.peers[peerID] = &PeerHealth{NodeID: peerID, Address: status.Address}
    p = fd.peers[peerID]

  p.MissedHeartbeats = 0
  p.LastHeartbeat = now()
  p.LastStatus = status
  fd.updateState(p)
```

## 8. Persistence Model

The failure detector is entirely in-memory. Peer states are not persisted; all peers start as UNKNOWN and transition to ACTIVE once the first successful heartbeat is exchanged. On restart, a node re-discovers its peers via the static seed list or gossip.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `sync.RWMutex` on peers map | RLock for reads (`ActivePeers`, `PeerState`); Lock for mutations (`RecordHeartbeat`, `sendHeartbeatTo`, `updateState`) |
| One goroutine per peer per tick | Heartbeats are sent in parallel to all peers |
| Async `onChange` callback | Called in a goroutine to avoid deadlock (mu must not be held when notifying membership) |

## 10. Configuration

```go
type FailureDetectorConfig struct {
    // HeartbeatInterval is how often heartbeats are sent to each peer.
    HeartbeatInterval time.Duration `yaml:"heartbeat_interval" default:"1s"`
    // HeartbeatTimeout is the deadline for a single heartbeat RPC.
    HeartbeatTimeout time.Duration `yaml:"heartbeat_timeout" default:"500ms"`
    // SuspectThreshold is the number of consecutive missed heartbeats to mark SUSPECT.
    SuspectThreshold int `yaml:"suspect_threshold" default:"3"`
    // FailureThreshold is the number of consecutive missed heartbeats to mark FAILED.
    FailureThreshold int `yaml:"failure_threshold" default:"5"`
}
```

## 11. Observability

- `goquorum_fd_heartbeat_sent_total{node}` — Counter of heartbeats sent per peer
- `goquorum_fd_heartbeat_received_total{node}` — Counter of heartbeats received per peer
- `goquorum_fd_heartbeat_failure_total{node}` — Counter of failed heartbeat RPCs per peer
- `goquorum_fd_state_transition_total{node,state}` — Counter of state transitions per peer
- `goquorum_fd_missed_heartbeats{node}` — Gauge of current missed heartbeat count per peer
- `goquorum_fd_active_peer_count` — Gauge of nodes in ACTIVE state
- `goquorum_fd_failed_peer_count` — Gauge of nodes in FAILED state
- Log at WARN when a peer transitions to SUSPECT
- Log at ERROR when a peer transitions to FAILED
- Log at INFO when a peer recovers to ACTIVE

## 12. Testing Strategy

- **Unit tests**:
  - `TestHeartbeatResetsCounter`: send successful heartbeat, assert missedHeartbeats=0 and state=ACTIVE
  - `TestMissedHeartbeatsSuspect`: fail `SuspectThreshold` heartbeats, assert state=SUSPECT
  - `TestMissedHeartbeatsFailure`: fail `FailureThreshold` heartbeats, assert state=FAILED
  - `TestRecoveryFromFailed`: node in FAILED receives heartbeat, assert state=ACTIVE and onChange called
  - `TestStateChangeCallbackFired`: state transition, assert onChange called with correct args
  - `TestAddRemovePeer`: add then remove a peer, assert it no longer appears in ActivePeers
  - `TestRecordHeartbeatAutoRegisters`: receive heartbeat from unknown peer, assert auto-registered
  - `TestConcurrentHeartbeats`: multiple goroutines sending heartbeats concurrently, assert no race
- **Integration tests**:
  - `TestFailureDetectionEndToEnd`: stop a node, wait for failure threshold, assert remaining nodes mark it FAILED
  - `TestNodeRecovery`: stop and restart a node, assert it returns to ACTIVE within 2 heartbeat cycles

## 13. Open Questions

None.
