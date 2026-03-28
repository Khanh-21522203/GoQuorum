---
name: Hinted Handoff
description: Durable write buffer that stores writes for offline nodes and replays them upon recovery
type: project
---

# Hinted Handoff

## Overview

`HintedHandoff` (`internal/cluster/hinted_handoff.go`) accepts writes that cannot be delivered to a replica because it is currently unreachable. It stores them in-memory as **hints** and replays them when the target node recovers. This allows writes to succeed during temporary failures without permanently sacrificing durability.

## Struct

```
hints       map[string][]*Hint   — keyed by target NodeID
membership  *MembershipManager
rpc         RPCClient
nodeID      string               — this node's ID
stopCh      chan struct{}
wg          sync.WaitGroup
mu          sync.RWMutex
```

**`Hint`**:
```
Key        string
Siblings   SiblingSet
CreatedAt  time.Time
```

## Constants

| Constant | Value | Meaning |
|---|---|---|
| `maxHintAge` | 24h | Discard hints older than this |
| `maxHintsPerNode` | 1000 | Max buffered hints per target |
| `hintReplayInterval` | 30s | How often to check for recovered nodes |

## Storing a Hint (`StoreHint`)

Called by `Coordinator.Put` for each failed replica:

```go
HintedHandoff.StoreHint(targetNodeID, key, siblings)
```

1. Rejects self-hints (`targetNodeID == nodeID`).
2. Copies `key` and `siblings` to avoid shared-memory issues.
3. If `len(hints[target]) >= maxHintsPerNode`: evicts the **oldest** hint (FIFO) and appends the new one.
4. Appends `&Hint{Key: key, Siblings: copy, CreatedAt: now}`.

The hint list is unbounded in node count (any failed node gets a slot) but capped per node at 1000 entries.

## Replay Loop

Background `replayLoop()` runs every `hintReplayInterval` (30s):

```
replayAll()
  │
  └─ for each targetNodeID with pending hints:
       if membership.GetPeerStatus(targetNodeID) == Active:
           replayHintsForNode(targetNodeID)
```

### `replayHintsForNode(nodeID)`

```
snapshot = copy of hints[nodeID]
for each hint in snapshot:
  if time.Since(hint.CreatedAt) > maxHintAge:
      drop (too old; data may be superseded)
      continue
  rpc.RemotePut(ctx with 5s timeout, nodeID, hint.Key, hint.Siblings)
    SUCCESS → remove from hints[nodeID]
    FAILURE → retain for next replay cycle
returns delivered count
```

Hints are not persisted to disk — a node restart loses buffered hints. Anti-entropy provides the recovery path for data that hints were unable to deliver before a restart.

## Integration Points

| Caller | Purpose |
|---|---|
| `Coordinator.Put` | `StoreHint` for each replica that returned an error |
| `cmd/quorum/main.go` | Wired to `FailureDetector.OnNodeRecovery` to allow immediate replay trigger |
| `Coordinator.Start` | Calls `hintedHandoff.Start()` to begin replay loop |
| `Coordinator.Stop` | Calls `hintedHandoff.Stop()` |

## Limitations

- **In-memory only**: Hints are lost on process restart. Anti-entropy handles the gap.
- **No deduplication**: If the same key is written multiple times while a node is down, all writes are buffered (up to 1000). Sibling reconciliation in the storage layer handles merging on replay.
- **Best-effort delivery**: If a hint replay fails (e.g., node recovered then failed again), it stays buffered until the next cycle.

Changes:

