---
name: Gossip & Membership
description: Gossip protocol for decentralized cluster state propagation and membership manager tracking peer health
type: project
---

# Gossip & Membership

## Overview

Two tightly coupled components manage cluster membership:

1. **`MembershipManager`** (`internal/cluster/membership.go`) — authoritative in-process state of all cluster nodes (statuses, addresses, latency).
2. **`Gossip`** (`internal/cluster/gossip.go`) — spreads node state updates across the cluster using a push-pull gossip protocol over HTTP.

## MembershipManager

### Struct

```
localMeta  LocalMetadata            — this node's ID, addr, start time, version, status
peers      map[string]*PeerMetadata — all other nodes keyed by NodeID
metrics    *MembershipMetrics
mu         sync.RWMutex
```

### PeerMetadata

```
NodeID       string
Addr         string    — gRPC address for client connections
HTTPAddr     string    — internal RPC address for node-to-node calls
Status       NodeStatus
LastSeen     time.Time
MissedCount  int
LatencyP99   float64   — milliseconds, rolling P99
```

### Node State Machine

```
Unknown / Joining
      │
      ▼ (heartbeat success)
   Active  ◄──────────────────────────────────┐
      │                                        │
      │ (heartbeat failure)                    │ (heartbeat success after failure)
      ▼                                        │
   Suspect  (1 ≤ MissedCount < FailureThreshold)
      │
      │ (MissedCount ≥ FailureThreshold)
      ▼
   Failed ──────────────────────────────────────┘
      │
      │ (graceful shutdown)
      ▼
   Leaving
```

`RecordHeartbeatSuccess(nodeID, latency)`:
- Resets `MissedCount = 0`.
- Updates `LatencyP99`.
- Transitions `Failed → Active` (auto-recovery).

`RecordHeartbeatFailure(nodeID)`:
- Increments `MissedCount`.
- If `MissedCount == 1`: status → `Suspect`.
- If `MissedCount ≥ FailureThreshold` (default 5): status → `Failed`.

### Quorum Helpers

`HasQuorum()`: Returns `true` if `activePeerCount+1 >= (totalNodes/2)+1`. Includes self.

`ActivateIfQuorum()`: Atomically transitions local node status from `Joining → Active` if quorum is reached. Called during bootstrap. Returns whether activation occurred.

### Key Accessors

| Function | Returns |
|---|---|
| `GetActivePeers()` | All peers with `Status == Active` |
| `GetAllPeers()` | All peers regardless of status |
| `GetPeerAddr(nodeID)` | gRPC address string |
| `GetHTTPAddress(nodeID)` | Internal HTTP address string |
| `GetClusterView()` | `map[string]NodeStatus` snapshot |
| `ActivePeerCount()` | Count of active peers (excluding self) |
| `AddPeer(cfg MemberConfig)` | Dynamically add peer |
| `RemovePeer(nodeID)` | Remove peer from tracking |

## Gossip Protocol

### Config

```go
type GossipConfig struct {
    Enabled    bool
    FanOut     int           // default 3: peers per round
    Interval   time.Duration // default 1s: rounds per second
    HTTPClient *http.Client  // 2s timeout
}
```

### NodeEntry (gossip state unit)

```
NodeID     string
Addr       string    — internal HTTP address
Status     NodeStatus
Version    int64     — monotonically increasing; used for LWW
UpdatedAt  int64     — Unix seconds; LWW tiebreak
```

### Gossip Round (`gossip.go:spreadLoop`)

Every `Interval`:
1. `gossipRound()` selects up to `FanOut` random peers from membership.
2. For each peer, calls `exchange(peer)`:
   - HTTP POST local state to `http://<peer>/internal/gossip/exchange`.
   - Receives peer's state in response.
   - Calls `Merge(peerState)`.

### Merge (Last-Write-Wins)

`Merge(incoming map[string]*NodeEntry)`:
- For each incoming entry:
  - If not in local state, or `incoming.UpdatedAt > local.UpdatedAt`: overwrite local entry.
  - Calls `membership.UpdatePeerStatus(nodeID, status)` to propagate into `MembershipManager`.

This is a **last-write-wins (LWW)** strategy keyed on `UpdatedAt`. `Version` acts as a lamport clock within a node to distinguish stale retransmissions from actual updates.

### Mark and Self-Update

`MarkPeer(nodeID, status)` — called by `FailureDetector` when it detects a peer state change; updates gossip state.

`SetSelf(status)` — increments `Version` and updates self entry; called on local status changes.

### Transport

HTTP JSON to `POST /internal/gossip/exchange`. The gossip exchange endpoint is registered by the HTTP server (handled in `internal/server`). TLS optional.

## Initialization (`bootstrap.go`, `cmd/quorum/main.go`)

1. `MembershipManager` is constructed with all cluster members from static config (`ClusterConfig.Members`).
2. `Gossip` is constructed referencing `MembershipManager`.
3. `Gossip.Start()` begins spreading.
4. `Bootstrap()` polls `HasQuorum()` until `BootstrapTimeout` (60s default).

## Metrics (`internal/cluster/membership_metrics.go`)

| Metric | Type | Description |
|---|---|---|
| `PeersTotal` | Gauge | Total configured peers |
| `PeersActive` | Gauge | Currently active peers |
| `PeersFailed` | Gauge | Currently failed peers |
| `PeersRecovered` | Counter | Recovery events |
| `PeerStateChanges` | Counter | All state transitions |

Changes:

