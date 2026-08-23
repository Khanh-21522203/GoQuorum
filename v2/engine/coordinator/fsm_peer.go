package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/membership"
)

// peerTrigger drives a single peer's NodeState transitions in Coordinator.
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

func (c *Coordinator) newPeerEntry(id node.NodeID) *peerEntry {
	return &peerEntry{
		id:     id,
		health: &node.NodeHealth{NodeID: id, State: node.NodeStateActive},
		state:  node.NodeStateActive,
	}
}

// OnHeartbeatResult implements failuredetector.ProbeHandler.
func (c *Coordinator) OnHeartbeatResult(nodeID node.NodeID, err error) {
	entry, ok := c.peers[nodeID]
	if !ok {
		entry = c.newPeerEntry(nodeID)
		c.peers[nodeID] = entry
	}

	if err != nil {
		entry.misses++
		entry.health.MissedHeartbeats = entry.misses
		if entry.misses >= c.failureDetectorConfig.FailureThreshold {
			c.handlePeerTrigger(entry, triggerThresholdReached)
		} else {
			c.handlePeerTrigger(entry, triggerHeartbeatMissed)
		}
		return
	}

	entry.misses = 0
	entry.health.MissedHeartbeats = 0
	entry.health.LastHeartbeat = time.Now()
	c.handlePeerTrigger(entry, triggerHeartbeatOK)
}

func (c *Coordinator) handlePeerTrigger(p *peerEntry, trigger peerTrigger) {
	switch p.state {
	case node.NodeStateUnknown:
		switch trigger {
		case triggerHeartbeatOK:
			c.transitionPeer(p, node.NodeStateActive)
		case triggerHeartbeatMissed:
			c.transitionPeer(p, node.NodeStateUnknown)
		case triggerThresholdReached:
			c.transitionPeer(p, node.NodeStateFailed)
		}

	case node.NodeStateActive:
		switch trigger {
		case triggerHeartbeatOK:
			// stay Active
		case triggerHeartbeatMissed:
			c.transitionPeer(p, node.NodeStateDegraded)
		case triggerThresholdReached:
			c.transitionPeer(p, node.NodeStateFailed)
		}

	case node.NodeStateDegraded:
		switch trigger {
		case triggerHeartbeatOK:
			c.transitionPeer(p, node.NodeStateActive)
		case triggerHeartbeatMissed:
			// stay Degraded
		case triggerThresholdReached:
			c.transitionPeer(p, node.NodeStateFailed)
		}

	case node.NodeStateFailed:
		switch trigger {
		case triggerHeartbeatOK:
			c.transitionPeer(p, node.NodeStateActive)
		case triggerHeartbeatMissed, triggerThresholdReached:
			// stay Failed
		}

	case node.NodeStateLeaving:
		switch trigger {
		case triggerThresholdReached:
			c.transitionPeer(p, node.NodeStateFailed)
		}
	}
}

func (c *Coordinator) transitionPeer(p *peerEntry, next node.NodeState) {
	prev := p.state
	p.state = next
	p.health.State = next
	c.enterPeerState(p, prev, next)
}

func (c *Coordinator) enterPeerState(p *peerEntry, from, to node.NodeState) {
	switch to {
	case node.NodeStateActive:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(p.id, membership.NodeStatusActive)
		}
		_ = c.ring.UpdateNodeState(p.id, node.NodeStateActive)
		if from == node.NodeStateFailed && c.handoff != nil {
			c.handoff.Replay([]node.NodeID{p.id})
		}
	case node.NodeStateDegraded:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(p.id, membership.NodeStatusSuspect)
		}
		_ = c.ring.UpdateNodeState(p.id, node.NodeStateDegraded)
	case node.NodeStateFailed:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(p.id, membership.NodeStatusFailed)
		}
		_ = c.ring.UpdateNodeState(p.id, node.NodeStateFailed)
	case node.NodeStateLeaving:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(p.id, membership.NodeStatusLeaving)
		}
		_ = c.ring.UpdateNodeState(p.id, node.NodeStateLeaving)
	}
}

// OnGossipReceived implements gossip.GossipHandler.
func (c *Coordinator) OnGossipReceived(peerID node.NodeID, entries []adapter.GossipEntry) {
	if c.membership == nil {
		return
	}
	for _, entry := range entries {
		if entry.NodeID == c.nodeID {
			continue
		}
		status := membership.NodeStatus(entry.Status)
		c.membership.UpdatePeerStatus(entry.NodeID, status)
		var nodeState node.NodeState
		switch status {
		case membership.NodeStatusActive:
			nodeState = node.NodeStateActive
		case membership.NodeStatusSuspect:
			nodeState = node.NodeStateDegraded
		case membership.NodeStatusFailed:
			nodeState = node.NodeStateFailed
		case membership.NodeStatusLeaving:
			nodeState = node.NodeStateLeaving
		default:
			nodeState = node.NodeStateUnknown
		}
		_ = c.ring.UpdateNodeState(entry.NodeID, nodeState)
	}
}
