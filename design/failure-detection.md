---
name: Failure Detection
description: Heartbeat-based peer liveness monitoring with partition detection and recovery callbacks
type: project
---

# Failure Detection

## Overview

`FailureDetector` (`internal/cluster/failure_detector.go`) sends periodic heartbeats to all peers and updates `MembershipManager` based on success or failure. It also fires callbacks when nodes transition to failed or recover, enabling the coordinator to trigger anti-entropy and hinted handoff replay.

## Struct

```
config      FailureDetectorConfig
peers       map[string]*NodeHealth   — local health state per peer
membership  *MembershipManager
rpc         RPCClient
metrics     *FailureDetectorMetrics
OnNodeRecovery  func(nodeID string)  — callback: node came back
OnNodeFailed    func(nodeID string)  — callback: node went down
stopCh      chan struct{}
```

`NodeHealth` (from `internal/common/node_state.go`):
```
NodeID           string
State            NodeState
LastHeartbeat    time.Time
MissedHeartbeats int
LastLatency      time.Duration
```

## Heartbeat Loop

`Start(peerIDs []string)`:
1. Initializes `NodeHealth` entries for all peers.
2. Launches `heartbeatLoop()` goroutine.

`heartbeatLoop()` ticks every `HeartbeatInterval` (default 1s):
- Calls `sendHeartbeats()` which fans out to all peers concurrently (one goroutine per peer, bounded by `HeartbeatTimeout`).

## Per-Peer Heartbeat (`sendHeartbeat(nodeID)`)

```
rpc.SendHeartbeat(ctx with HeartbeatTimeout, nodeID)
  │
  ├─ SUCCESS:
  │   ├─ membership.RecordHeartbeatSuccess(nodeID, latency)
  │   ├─ If prior state was Failed → detectPartitionHealed(nodeID)
  │   │     → calls OnNodeRecovery(nodeID)
  │   └─ metrics.HeartbeatSuccess++
  │
  └─ FAILURE:
      ├─ membership.RecordHeartbeatFailure(nodeID)
      ├─ If new state is Failed → detectPartition(nodeID)
      │     → calls OnNodeFailed(nodeID)
      └─ metrics.HeartbeatFailure++
```

`detectPartition(failedNodeID)`: Logs suspected partition when the target is unreachable but other peers remain healthy. Increments `PartitionSuspected` metric.

`detectPartitionHealed(nodeID)`: Logs partition healed. Increments `PartitionHealed` metric.

## Failure Detection Time

`FailureDetectorConfig.DetectionTime()`:
```
DetectionTime = FailureThreshold × (HeartbeatInterval + HeartbeatTimeout)
// Default: 5 × (1s + 2s) = 15 seconds
```

## Configuration (`internal/config/failure_detector.go`)

| Field | Default | Description |
|---|---|---|
| `HeartbeatInterval` | 1s | How often to ping peers |
| `HeartbeatTimeout` | 2s | Per-ping deadline |
| `FailureThreshold` | 5 | Missed pings before `Failed` |
| `ReplicaRPCTimeout` | 2s | Timeout for replica read/write RPCs |
| `SlowNodeLatencyThreshold` | 1s | P99 latency to flag slow node |
| `SlowNodeTimeoutThreshold` | 10% | Timeout rate to flag slow node |

Preset configs:
- `Aggressive`: 500ms interval, 1s timeout, threshold 3 (detects failure in ~4.5s)
- `Conservative`: 2s interval, 5s timeout, threshold 10 (detects failure in ~70s; avoids false positives in high-latency nets)

## Callbacks and Their Effects

`OnNodeRecovery(nodeID)` is wired in `main.go` to:
- `antiEntropy.TriggerWithPeer(nodeID)` — kick off async data sync with recovered node.

`OnNodeFailed(nodeID)` is wired in `main.go` to:
- Log partition; suspend hinted handoff replay for that node until it recovers.

## State Accessor Functions

| Function | Returns |
|---|---|
| `GetHealthyNodes()` | `[]string` — peer IDs with `State == Active` |
| `GetNodeState(nodeID)` | `NodeState` |
| `IsNodeHealthy(nodeID)` | `bool` |
| `GetNodeHealth(nodeID)` | `NodeHealth` copy |
| `UpdateNodeState(nodeID, state)` | Manual override (used by graceful shutdown to mark node as `Leaving`) |

## Metrics (`internal/cluster/failure_detector_metrics.go`)

| Metric | Type | Description |
|---|---|---|
| `HeartbeatSuccess` | Counter | Successful pings |
| `HeartbeatFailure` | Counter | Failed pings |
| `NodeFailed` | Counter | Transitions to failed |
| `NodeRecovered` | Counter | Transitions to active after failure |
| `SlowNodeDetected` | Counter | P99 or timeout-rate threshold crossed |
| `PartitionSuspected` | Counter | Partition events detected |
| `PartitionHealed` | Counter | Partition healed events |
| `PeerLatency` | Histogram per peer | Round-trip heartbeat latency per node |

Changes:

