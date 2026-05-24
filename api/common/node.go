package common

import (
	"sync"
	"time"
)

type NodeID string

// Validate checks if the NodeID meets constraints
func (n NodeID) Validate() bool {
	if len(n) < 1 || len(n) > 64 {
		return false
	}
	// Alphanumeric plus hyphen and underscore
	for _, r := range n {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Node represents a physical node in the cluster
type Node struct {
	ID               NodeID
	Addr             string // host:port
	State            NodeState
	VirtualNodeCount int

	// Failure detection
	MissedHeartbeats int
	LastHeartbeat    time.Time

	mu sync.RWMutex
}

func (n *Node) UpdateState(state NodeState) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.State = state
}

func (n *Node) GetState() NodeState {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.State
}

func (n *Node) RecordHeartbeat() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.LastHeartbeat = time.Now()
	n.MissedHeartbeats = 0
	n.State = NodeStateActive
}

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
