# Feature: Sloppy Quorum

## 1. Purpose

Sloppy quorum is a write-availability enhancement that complements hinted handoff. In the standard (strict) quorum model, a write must be acknowledged by W nodes from the key's preference list. With sloppy quorum enabled, when fewer than W preferred nodes are available, the coordinator may accept writes from nodes outside the preference list ("helper nodes"), count them toward quorum, and store hints for the unavailable preferred nodes. This significantly improves write availability during partial failures at the cost of temporarily weaker durability guarantees. This feature is not yet implemented.

## 2. Responsibilities

- **Overflow node selection**: When fewer than W preferred nodes are reachable, select additional "helper" nodes from the rest of the ring to absorb the write
- **Hinted write**: Write to helper nodes tagged with the hint metadata (`intended_for: <preferred_node_id>`)
- **Quorum accounting**: Count helper writes as fulfilling the W quorum requirement
- **Coordinator integration**: Extend the Coordinator's Put/Delete fanout to include helper nodes when strict quorum cannot be met
- **Configuration**: Allow sloppy quorum to be enabled/disabled independently of hinted handoff

## 3. Non-Responsibilities

- Does not implement the hint storage or delivery (delegated to `HintedHandoff`)
- Does not change Get behavior — reads still use the preference list; helper nodes are not read from
- Does not implement the ring traversal for overflow nodes (uses existing `HashRing`)
- Does not guarantee that the full preference list will always be used once sloppy quorum enables fallback

## 4. Architecture Design

```
Standard (strict) quorum Put with N=3, W=2:
  Preference list: [A, B, C]
  A: success
  B: failed (down)
  C: success
  → W=2 met with A + C ✓

Sloppy quorum Put with N=3, W=2, B is down:
  Preference list: [A, B, C]
  A: success
  B: failed (down)
  C: success
  → W=2 already met → no helper needed (same as above)

Sloppy quorum Put with N=3, W=2, B AND C are down:
  Preference list: [A, B, C]
  A: success
  B: failed (down) → StoreHint(key, sibSet, targetNode=B)
  C: failed (down) → StoreHint(key, sibSet, targetNode=C)
  → only 1 success from preference list
  → select helper D (next live node on ring after C)
  D: success (with hint metadata: intended_for=B or C)
  → W=2 met with A + D ✓
```

### Helper Node Selection
```
ring.PreferenceList(key, N)  →  [A, B, C]  (preferred)
ring.PreferenceList(key, N+numDown)  →  [A, B, C, D, E]
helper = first live node in extended list not in preferred list
       = D
```

## 5. Core Data Structures (Go)

```go
package cluster

// SloppyQuorumConfig controls sloppy quorum behavior.
type SloppyQuorumConfig struct {
    // Enabled controls whether sloppy quorum is used.
    // When false, Put/Delete fail immediately if fewer than W preferred nodes are reachable.
    Enabled bool `yaml:"enabled" default:"false"`
    // MaxHelperNodes is the maximum number of helper nodes to try.
    // Limits how far down the ring we search for overflow capacity.
    MaxHelperNodes int `yaml:"max_helper_nodes" default:"3"`
}

// fanoutResult extends replicaResult with sloppy quorum metadata.
type fanoutResult struct {
    nodeID     string
    vclock     *vclock.VClock
    err        error
    isHelper   bool   // true if this node is a helper, not on the preference list
    intendedFor string // for helper nodes: the preferred node ID they substituted for
}
```

## 6. Public Interfaces

```go
package cluster

// The sloppy quorum logic is embedded in Coordinator.Put and Coordinator.Delete.
// No new public types are exposed; behavior is controlled via CoordinatorConfig.

// CoordinatorConfig is extended with:
type CoordinatorConfig struct {
    // ... existing fields ...
    SloppyQuorum SloppyQuorumConfig `yaml:"sloppy_quorum"`
}
```

## 7. Internal Algorithms

### Extended Fanout with Sloppy Quorum
```
Put(ctx, key, value):
  // ... generate new sibling set ...

  prefList = ring.PreferenceList(key, N)
  results = make(chan fanoutResult, N + config.SloppyQuorum.MaxHelperNodes)

  // Fanout to all preferred nodes
  for each node in prefList:
    go fanoutReplicate(ctx, node, key, newSibSet, results, isHelper=false)

  successes = 0
  failures  = []nodeID{}
  delivered = map[nodeID]bool{}

  // Collect results, track failures
  for i in 0..N:
    r = <-results
    delivered[r.nodeID] = true
    if r.err == nil:
      successes++
    else:
      failures = append(failures, r.nodeID)

  // If strict quorum met, return early
  if successes >= W:
    return newSib.VClock, nil

  // Sloppy quorum: try helper nodes if enabled
  if !config.SloppyQuorum.Enabled || len(failures) == 0:
    return nil, QuorumError{...}

  // Select helper nodes: live nodes on the ring not in prefList
  helpers = selectHelpers(key, prefList, failures, config.SloppyQuorum.MaxHelperNodes)

  for _, helper := range helpers:
    // Store hint metadata alongside the sibling set for this helper
    go fanoutReplicateWithHint(ctx, helper, key, newSibSet, failures[0], results)

    r = <-results
    if r.err == nil:
      successes++
      // Register the hint for the intended preferred node
      hintedHandoff.StoreHint(key, newSibSet, r.intendedFor)
      if successes >= W:
        return newSib.VClock, nil

  return nil, QuorumError{W, successes, errors}
```

### selectHelpers
```
selectHelpers(key, prefList, failedNodes, maxHelpers):
  prefSet = set(prefList IDs)
  failedSet = set(failedNodes)

  // Extended preference list to find overflow capacity
  extended = ring.PreferenceList(key, N + maxHelpers * 2)  // over-fetch to account for more failures

  helpers = []NodeInfo{}
  for node in extended:
    if node.ID in prefSet: continue      // skip preferred nodes (already tried)
    if node.State == NodeStateFailed: continue  // skip dead nodes
    helpers = append(helpers, node)
    if len(helpers) == maxHelpers: break

  return helpers
```

## 8. Persistence Model

Sloppy quorum itself has no persistence. The sloppy quorum decision is made per-request and is not recorded. The side effect — storing a hint — is persisted by the `HintedHandoff` subsystem (see `plan-hinted-handoff.md`).

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Extended results channel | Sized to `N + MaxHelperNodes`; avoids goroutine leaks |
| Coordinator key lock | Still held during the full fanout (preferred + helpers) to serialize vector clock generation |
| HintedHandoff.StoreHint | Called asynchronously after the sloppy quorum is achieved |

## 10. Configuration

```go
type SloppyQuorumConfig struct {
    Enabled        bool `yaml:"enabled" default:"false"`
    MaxHelperNodes int  `yaml:"max_helper_nodes" default:"3"`
}
```

Sloppy quorum is disabled by default to preserve strict Dynamo semantics. Enable it when high write availability is more important than strict preference list adherence.

## 11. Observability

- `goquorum_sloppy_quorum_activations_total` — Counter of writes that fell back to sloppy quorum
- `goquorum_sloppy_quorum_helper_writes_total{helper_node}` — Counter of writes to each helper node
- `goquorum_sloppy_quorum_quorum_failures_total` — Counter of writes that failed even with sloppy quorum
- Log at INFO when sloppy quorum is activated for a write, including key and helper nodes used

## 12. Testing Strategy

- **Unit tests**:
  - `TestSloppyQuorumNotActivatedWhenStrict`: W preferred nodes available, assert no helper used
  - `TestSloppyQuorumActivatesWhenNeeded`: W-1 preferred nodes available, assert helper selected
  - `TestSloppyQuorumHintStored`: helper write succeeds, assert StoreHint called for failed preferred node
  - `TestSloppyQuorumHelperSelection`: assert helper is not in preference list and is ACTIVE
  - `TestSloppyQuorumDisabled`: enabled=false, assert QuorumError returned even when helpers available
  - `TestMaxHelperNodesRespected`: only MaxHelperNodes helpers tried
  - `TestSloppyQuorumFailsIfNoHelpers`: all nodes down, assert QuorumError
- **Integration tests**:
  - `TestSloppyQuorumE2E3Nodes`: 3-node cluster, down 2 nodes, assert write succeeds with sloppy quorum; bring nodes back, assert hints delivered

## 13. Open Questions

- Should reads also use sloppy quorum (read from helper nodes holding hints)? This increases complexity but improves read availability during severe partitions.
- Should the API response indicate when sloppy quorum was used (e.g., a warning field in PutResponse)?
