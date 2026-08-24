package node

import (
	"sync"
	"time"
)

// NodeID uniquely identifies a node in the cluster.
type NodeID string

// Validate reports whether the NodeID meets the length and character
// constraints: 1-64 characters, alphanumeric plus '-' and '_'.
func (n NodeID) Validate() bool {
	if len(n) < 1 || len(n) > 64 {
		return false
	}
	for _, r := range n {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Node represents a physical node in the cluster.
type Node struct {
	ID               NodeID
	Addr             string // host:port
	State            NodeState
	VirtualNodeCount int

	// Failure detection.
	MissedHeartbeats int
	LastHeartbeat    time.Time

	mu sync.RWMutex // guards State, MissedHeartbeats, LastHeartbeat
}

// UpdateState sets the node's current state.
func (n *Node) UpdateState(state NodeState) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.State = state
}

// GetState returns the node's current state.
func (n *Node) GetState() NodeState {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.State
}

// RecordHeartbeat resets missed-heartbeat tracking and marks the node active.
func (n *Node) RecordHeartbeat() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.LastHeartbeat = time.Now()
	n.MissedHeartbeats = 0
	n.State = NodeStateActive
}

// RecordMissedHeartbeat increments the missed-heartbeat counter, demoting the
// node to Degraded, or to Failed once threshold is reached.
func (n *Node) RecordMissedHeartbeat(threshold int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.MissedHeartbeats++

	if n.MissedHeartbeats >= threshold {
		n.State = NodeStateFailed
	} else {
		n.State = NodeStateDegraded
	}
}
