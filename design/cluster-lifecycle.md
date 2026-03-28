---
name: Cluster Lifecycle
description: Bootstrap sequence, dynamic node join/leave, and graceful shutdown
type: project
---

# Cluster Lifecycle

## Overview

This covers three lifecycle events: how a node starts up and joins the cluster, how nodes dynamically join or leave at runtime, and how a node shuts down cleanly.

## Bootstrap (`internal/cluster/bootstrap.go`)

`Bootstrap(cfg, membership, failureDetector) error`:

```
Step 1: Validate config (ClusterConfig, QuorumConfig, etc.)
Step 2: Set local status → Joining
Step 3: gRPC server already started (pre-condition from caller)
Step 4: Start FailureDetector with peer IDs from config
Step 5: Poll until quorum or BootstrapTimeout (default 60s):
    every 1s:
        if membership.ActivateIfQuorum():
            local status → Active
            break
Step 6: Return nil (or error on timeout)
```

`peerIDsFromMembers(members)` extracts NodeIDs, filtering out self.

`ActivateIfQuorum()` (`membership.go`) atomically tests whether `activePeerCount + 1 >= (totalNodes/2)+1` and if so transitions local status from `Joining → Active`. Returns `true` on transition.

**Timeout behavior**: If quorum is not reached within `BootstrapTimeout`, `Bootstrap` returns an error and the server exits. This prevents split-brain from a single node declaring itself active.

## Dynamic Join (`Coordinator.JoinNode`)

Called when a new node is added to the cluster at runtime (not at startup):

```
Coordinator.JoinNode(node *common.Node)
  │
  ├─ ring.AddNode(node)         — register in hash ring
  ├─ membership.AddPeer(...)    — start tracking in membership
  └─ antiEntropy.TriggerWithPeer(node.ID)  — async: sync data to new node
```

The ring immediately starts routing new writes to the joining node. Anti-entropy delivers existing keys that belong to the new node's ranges.

## Dynamic Leave (`Coordinator.LeaveNode`)

Called when a node is being removed (graceful or detected failure):

```
Coordinator.LeaveNode(nodeID string)
  │
  ├─ antiEntropy.SyncWithPeers([remaining peers]) — drain: push all data
  ├─ ring.RemoveNode(nodeID)    — stop routing to leaving node
  └─ membership.RemovePeer(nodeID)
```

`SyncWithPeers` is synchronous (blocks until all exchanges complete or timeout) to ensure data is handed off before the node stops routing.

## Graceful Shutdown (`internal/cluster/graceful_shutdown.go`)

`GracefulShutdown` (`graceful_shutdown.go`) manages the orderly teardown sequence:

```
Shutdown()
  │
  ├─ Step 1: Mark shutting down (IsShuttingDown = true)
  │           Health endpoint returns NOT_READY → load balancer stops sending
  │
  ├─ Step 2: Drain in-flight requests
  │           Poll coordinator.InFlightCount() every 10ms
  │           Timeout after drainTimeout (default 30s)
  │
  ├─ Step 3: Notify peers (best-effort, 5s timeout)
  │           rpc.NotifyLeaving() → peers mark this node as Leaving
  │
  ├─ Step 4: storage.Close() — flush WAL, close Pebble
  │           Returns error if flush fails
  │
  └─ Step 5: failureDetector.Stop() — halt heartbeat loop
```

`IsShuttingDown()` is an atomic flag checked by the health readiness endpoint to return 503 during drain.

Signal handling in `main.go`: `SIGTERM` / `SIGINT` → `gracefulShutdown.Shutdown()`.

### Drain Timeout

If in-flight requests do not drain within 30s, shutdown proceeds anyway. Requests in-flight after storage close will fail with `ErrStorageClosed`.

## Startup Sequence Summary (`cmd/quorum/main.go`)

```
1. LoadConfig(path)
2. NewStorage(storageOpts)
3. NewMembershipManager(clusterConfig)
4. NewHashRing() → AddNode for each member
5. NewGRPCClient(connectionConfig, membership)
6. NewCoordinator(ring, storage, rpc, membership, ...)
7. NewFailureDetector(config, membership, rpc)
   → OnNodeRecovery = antiEntropy.TriggerWithPeer
   → OnNodeFailed   = log + suspend hint replay
8. NewGossip(gossipConfig, membership)
9. NewTTLSweeper(storage, coordinator, interval)
10. Start HTTP + gRPC server
11. Bootstrap(cfg, membership, failureDetector)  — blocks until quorum
12. coordinator.Start()  — begins anti-entropy and hinted handoff loops
13. gossip.Start()
14. ttlSweeper.Start()
15. Wait for SIGTERM/SIGINT
16. gracefulShutdown.Shutdown()
```

Changes:

