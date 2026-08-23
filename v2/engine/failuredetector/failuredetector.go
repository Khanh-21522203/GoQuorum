package failuredetector

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/statemachine"
)

// lifecycleState is the failure detector's own run state, distinct from any
// tracked peer's NodeState.
type lifecycleState int

const (
	lifecycleIdle lifecycleState = iota
	lifecycleRunning
	lifecycleStopped
)

// lifecycleTrigger drives lifecycleState transitions.
type lifecycleTrigger int

const (
	lifecycleTriggerStart lifecycleTrigger = iota
	lifecycleTriggerStop
)

func newLifecycle() *statemachine.Machine[lifecycleState, lifecycleTrigger] {
	return statemachine.New(lifecycleIdle, []statemachine.Edge[lifecycleState, lifecycleTrigger]{
		{From: lifecycleIdle, Trigger: lifecycleTriggerStart, To: lifecycleRunning},
		{From: lifecycleRunning, Trigger: lifecycleTriggerStop, To: lifecycleStopped},
	})
}

// peerTrigger drives a single peer's node.NodeState machine. Consecutive
// missed heartbeats are counted outside the machine (see peerEntry.misses)
// and only turn into a trigger once they matter: HeartbeatMissed for a miss
// still under the failure threshold, ThresholdReached once the threshold is
// crossed. This keeps the state space to the five node.NodeState values
// instead of one state per possible miss count.
type peerTrigger int

const (
	triggerHeartbeatOK peerTrigger = iota
	triggerHeartbeatMissed
	triggerThresholdReached
)

// peerEntry bundles a tracked peer's heartbeat bookkeeping with the state
// machine that owns its current node.NodeState.
type peerEntry struct {
	health  *node.NodeHealth
	machine *statemachine.Machine[node.NodeState, peerTrigger]
	misses  int // consecutive missed heartbeats since the last success.
}

// FailureDetector monitors peer liveness by sending periodic heartbeats
// over the transport port and escalating a peer's node.NodeState after
// enough are missed.
type FailureDetector struct {
	config     config.FailureDetectorConfig
	peers      map[node.NodeID]*peerEntry
	membership *membership.MembershipManager
	transport  adapter.Transport
	reactor    *reactor.Reactor

	lifecycle *statemachine.Machine[lifecycleState, lifecycleTrigger]
	timerID   reactor.TimerID

	// OnNodeRecovery fires once when a peer transitions from
	// NodeStateFailed back to NodeStateActive.
	OnNodeRecovery func(nodeID node.NodeID)
	// OnNodeFailed fires once when a peer is first confirmed failed.
	OnNodeFailed func(nodeID node.NodeID)
}

// NewFailureDetector creates a failure detector for the given configuration,
// membership view, transport, and reactor. Heartbeats are scheduled on rc,
// so Start/Stop/tick must run on rc's own goroutine, same as any other
// reactor-owned state.
func NewFailureDetector(cfg config.FailureDetectorConfig, mm *membership.MembershipManager, tr adapter.Transport, rc *reactor.Reactor) *FailureDetector {
	return &FailureDetector{
		config:     cfg,
		peers:      make(map[node.NodeID]*peerEntry),
		membership: mm,
		transport:  tr,
		reactor:    rc,
		lifecycle:  newLifecycle(),
	}
}

// peerEdges builds the fixed (state, trigger) -> state table shared by every
// peer's machine, with OnNodeFailed/OnNodeRecovery actions bound to id.
// UpdateNodeState reuses this same builder so a manually forced state still
// obeys the normal heartbeat-driven transitions afterward.
func (fd *FailureDetector) peerEdges(id node.NodeID) []statemachine.Edge[node.NodeState, peerTrigger] {
	recoverAction := func() error {
		if fd.OnNodeRecovery != nil {
			fd.OnNodeRecovery(id)
		}
		return nil
	}
	failAction := func() error {
		if fd.OnNodeFailed != nil {
			fd.OnNodeFailed(id)
		}
		return nil
	}
	return []statemachine.Edge[node.NodeState, peerTrigger]{
		// A successful heartbeat always resolves to Active. Recovery is
		// defined as specifically leaving Failed, so only that edge carries
		// the OnNodeRecovery action; a peer degraded by a handful of misses
		// but never fully failed recovers silently.
		{From: node.NodeStateUnknown, Trigger: triggerHeartbeatOK, To: node.NodeStateActive},
		{From: node.NodeStateActive, Trigger: triggerHeartbeatOK, To: node.NodeStateActive},
		{From: node.NodeStateDegraded, Trigger: triggerHeartbeatOK, To: node.NodeStateActive},
		{From: node.NodeStateFailed, Trigger: triggerHeartbeatOK, To: node.NodeStateActive, Action: recoverAction},
		// A peer forced into Leaving by UpdateNodeState is expected to stop
		// participating on its own terms; heartbeat successes don't pull it
		// back to Active behind the caller's back.
		{From: node.NodeStateLeaving, Trigger: triggerHeartbeatOK, To: node.NodeStateLeaving},

		// A miss below FailureThreshold only demotes an Active peer to
		// Degraded. Other states are left alone until ThresholdReached fires.
		{From: node.NodeStateUnknown, Trigger: triggerHeartbeatMissed, To: node.NodeStateUnknown},
		{From: node.NodeStateActive, Trigger: triggerHeartbeatMissed, To: node.NodeStateDegraded},
		{From: node.NodeStateDegraded, Trigger: triggerHeartbeatMissed, To: node.NodeStateDegraded},
		{From: node.NodeStateLeaving, Trigger: triggerHeartbeatMissed, To: node.NodeStateLeaving},

		// Crossing FailureThreshold consecutive misses escalates to Failed
		// exactly once; the Failed -> Failed edge absorbs every miss after
		// that without re-running the fail action.
		{From: node.NodeStateUnknown, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateActive, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateDegraded, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateLeaving, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateFailed, Trigger: triggerThresholdReached, To: node.NodeStateFailed},
	}
}

func (fd *FailureDetector) newPeerEntry(id node.NodeID) *peerEntry {
	return &peerEntry{
		health:  &node.NodeHealth{NodeID: id, State: node.NodeStateUnknown},
		machine: statemachine.New(node.NodeStateUnknown, fd.peerEdges(id)),
	}
}

// Start seeds health tracking for peerIDs and launches the heartbeat loop.
// Must be called from the reactor's own goroutine.
func (fd *FailureDetector) Start(peerIDs []node.NodeID) {
	for _, id := range peerIDs {
		fd.peers[id] = fd.newPeerEntry(id)
	}
	_ = fd.lifecycle.Handle(lifecycleTriggerStart)
	fd.timerID = fd.reactor.ScheduleEvery(fd.config.HeartbeatInterval, fd.tick)
}

// Stop halts the heartbeat loop. Must be called from the reactor's own
// goroutine.
func (fd *FailureDetector) Stop() {
	_ = fd.lifecycle.Handle(lifecycleTriggerStop)
	fd.reactor.CancelTimer(fd.timerID)
}

// tick fires one heartbeat round: every tracked peer is pinged, and its
// machine is driven by the outcome once the transport calls back.
func (fd *FailureDetector) tick() {
	for id, entry := range fd.peers {
		id, entry := id, entry
		fd.transport.Heartbeat(id, func(err error) {
			fd.onHeartbeatResult(id, entry, err)
		})
	}
}

// onHeartbeatResult updates a peer's bookkeeping and drives its machine from
// one heartbeat outcome. It runs on the reactor goroutine, since that's the
// only goroutine transport.Heartbeat's done callback is ever invoked from.
func (fd *FailureDetector) onHeartbeatResult(id node.NodeID, entry *peerEntry, err error) {
	if err != nil {
		entry.misses++
		entry.health.MissedHeartbeats = entry.misses
		if entry.misses >= fd.config.FailureThreshold {
			_ = entry.machine.Handle(triggerThresholdReached)
		} else {
			_ = entry.machine.Handle(triggerHeartbeatMissed)
		}
		return
	}

	entry.misses = 0
	entry.health.MissedHeartbeats = 0
	entry.health.LastHeartbeat = time.Now()
	// transport.Heartbeat's callback carries only a success/failure error,
	// no round-trip timing, so LastLatency can't be populated from here.
	_ = entry.machine.Handle(triggerHeartbeatOK)
}

// GetHealthyNodes returns the IDs of all peers currently considered
// healthy.
func (fd *FailureDetector) GetHealthyNodes() []node.NodeID {
	healthy := make([]node.NodeID, 0, len(fd.peers))
	for id, entry := range fd.peers {
		if entry.machine.State() == node.NodeStateActive {
			healthy = append(healthy, id)
		}
	}
	return healthy
}

// GetNodeState returns the current NodeState of the given peer.
func (fd *FailureDetector) GetNodeState(nodeID node.NodeID) node.NodeState {
	entry, ok := fd.peers[nodeID]
	if !ok {
		return node.NodeStateUnknown
	}
	return entry.machine.State()
}

// IsNodeHealthy reports whether the given peer is currently healthy.
func (fd *FailureDetector) IsNodeHealthy(nodeID node.NodeID) bool {
	return fd.GetNodeState(nodeID) == node.NodeStateActive
}

// GetNodeHealth returns a copy of the tracked health record for the given
// peer, or nil if not tracked.
func (fd *FailureDetector) GetNodeHealth(nodeID node.NodeID) *node.NodeHealth {
	entry, ok := fd.peers[nodeID]
	if !ok {
		return nil
	}
	return &node.NodeHealth{
		NodeID:           entry.health.NodeID,
		State:            entry.machine.State(),
		LastHeartbeat:    entry.health.LastHeartbeat,
		MissedHeartbeats: entry.health.MissedHeartbeats,
		LastLatency:      entry.health.LastLatency,
	}
}

// UpdateNodeState manually sets a peer's state, bypassing heartbeat
// tracking (used during graceful shutdown/leave).
func (fd *FailureDetector) UpdateNodeState(nodeID node.NodeID, state node.NodeState) {
	entry, ok := fd.peers[nodeID]
	if !ok {
		return
	}
	// Machine has no forced-state escape hatch by design, so a manual
	// override replaces it outright: same edge table, new initial state.
	// Heartbeat bookkeeping (health, misses) is left untouched.
	entry.machine = statemachine.New(state, fd.peerEdges(nodeID))
}
