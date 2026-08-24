package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/membership"
)

// PeerTransitionHandler is invoked when a peer's NodeState changes.
type PeerTransitionHandler func(id node.NodeID, from, to node.NodeState)

type peerTrigger int

const (
	triggerHeartbeatOK peerTrigger = iota
	triggerHeartbeatMissed
	triggerThresholdReached
)

// peerEntry bundles a tracked peer's heartbeat bookkeeping and state.
type peerEntry struct {
	id     node.NodeID
	health *node.NodeHealth
	state  node.NodeState
	misses int
}

// PeerFSM is a pure finite state machine governing peer health states.
// It has zero dependencies on Coordinator, Storage, or HashRing.
type PeerFSM struct {
	threshold    int
	peers        map[node.NodeID]*peerEntry
	onTransition PeerTransitionHandler
}

// NewPeerFSM creates a new PeerFSM with the given failure threshold and transition hook.
func NewPeerFSM(threshold int, handler PeerTransitionHandler) *PeerFSM {
	if threshold <= 0 {
		threshold = 3
	}
	return &PeerFSM{
		threshold:    threshold,
		peers:        make(map[node.NodeID]*peerEntry),
		onTransition: handler,
	}
}

// AddPeer registers a new peer into the FSM with the given initial state.
func (fsm *PeerFSM) AddPeer(id node.NodeID, initialState node.NodeState) {
	if _, exists := fsm.peers[id]; !exists {
		fsm.peers[id] = &peerEntry{
			id:     id,
			health: &node.NodeHealth{NodeID: id, State: initialState},
			state:  initialState,
		}
	}
}

// GetPeer returns the peerEntry for id if present.
func (fsm *PeerFSM) GetPeer(id node.NodeID) (*peerEntry, bool) {
	p, ok := fsm.peers[id]
	return p, ok
}

// Peers returns the underlying map of peers.
func (fsm *PeerFSM) Peers() map[node.NodeID]*peerEntry {
	return fsm.peers
}

// OnHeartbeatResult processes a heartbeat probe result for nodeID.
func (fsm *PeerFSM) OnHeartbeatResult(nodeID node.NodeID, err error) {
	entry, ok := fsm.peers[nodeID]
	if !ok {
		entry = &peerEntry{
			id:     nodeID,
			health: &node.NodeHealth{NodeID: nodeID, State: node.NodeStateActive},
			state:  node.NodeStateActive,
		}
		fsm.peers[nodeID] = entry
	}

	if err != nil {
		entry.misses++
		entry.health.MissedHeartbeats = entry.misses
		if entry.misses >= fsm.threshold {
			fsm.handlePeerTrigger(entry, triggerThresholdReached)
		} else {
			fsm.handlePeerTrigger(entry, triggerHeartbeatMissed)
		}
		return
	}

	entry.misses = 0
	entry.health.MissedHeartbeats = 0
	entry.health.LastHeartbeat = time.Now()
	fsm.handlePeerTrigger(entry, triggerHeartbeatOK)
}

// OnGossipReceived processes gossiped peer states.
func (fsm *PeerFSM) OnGossipReceived(entries []adapter.GossipEntry, localID node.NodeID) {
	for _, entry := range entries {
		if entry.NodeID == localID {
			continue
		}
		status := membership.NodeStatus(entry.Status)
		var targetState node.NodeState
		switch status {
		case membership.NodeStatusActive:
			targetState = node.NodeStateActive
		case membership.NodeStatusSuspect:
			targetState = node.NodeStateDegraded
		case membership.NodeStatusFailed:
			targetState = node.NodeStateFailed
		case membership.NodeStatusLeaving:
			targetState = node.NodeStateLeaving
		default:
			targetState = node.NodeStateUnknown
		}

		p, ok := fsm.peers[entry.NodeID]
		if !ok {
			fsm.AddPeer(entry.NodeID, targetState)
			if fsm.onTransition != nil {
				fsm.onTransition(entry.NodeID, node.NodeStateUnknown, targetState)
			}
			continue
		}
		if p.state != targetState {
			fsm.transitionPeer(p, targetState)
		}
	}
}

func (fsm *PeerFSM) handlePeerTrigger(p *peerEntry, trigger peerTrigger) {
	switch p.state {
	case node.NodeStateUnknown:
		switch trigger {
		case triggerHeartbeatOK:
			fsm.transitionPeer(p, node.NodeStateActive)
		case triggerHeartbeatMissed:
			fsm.transitionPeer(p, node.NodeStateUnknown)
		case triggerThresholdReached:
			fsm.transitionPeer(p, node.NodeStateFailed)
		}

	case node.NodeStateActive:
		switch trigger {
		case triggerHeartbeatOK:
			// stay Active
		case triggerHeartbeatMissed:
			fsm.transitionPeer(p, node.NodeStateDegraded)
		case triggerThresholdReached:
			fsm.transitionPeer(p, node.NodeStateFailed)
		}

	case node.NodeStateDegraded:
		switch trigger {
		case triggerHeartbeatOK:
			fsm.transitionPeer(p, node.NodeStateActive)
		case triggerHeartbeatMissed:
			// stay Degraded
		case triggerThresholdReached:
			fsm.transitionPeer(p, node.NodeStateFailed)
		}

	case node.NodeStateFailed:
		switch trigger {
		case triggerHeartbeatOK:
			fsm.transitionPeer(p, node.NodeStateActive)
		case triggerHeartbeatMissed, triggerThresholdReached:
			// stay Failed
		}

	case node.NodeStateLeaving:
		switch trigger {
		case triggerThresholdReached:
			fsm.transitionPeer(p, node.NodeStateFailed)
		}
	}
}

func (fsm *PeerFSM) transitionPeer(p *peerEntry, next node.NodeState) {
	prev := p.state
	p.state = next
	p.health.State = next
	if fsm.onTransition != nil && prev != next {
		fsm.onTransition(p.id, prev, next)
	}
}
