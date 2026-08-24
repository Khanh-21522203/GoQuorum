package node

import "time"

// NodeState represents the health state of a node.
type NodeState int

const (
	NodeStateActive   NodeState = iota // Node is healthy and operational.
	NodeStateFailed                    // Node is unreachable.
	NodeStateDegraded                  // Node is slow or degraded.
	NodeStateLeaving                   // Node is gracefully shutting down.
	NodeStateUnknown                   // Initial state or unknown.
)

// String returns the human-readable name of the state.
func (s NodeState) String() string {
	switch s {
	case NodeStateActive:
		return "ACTIVE"
	case NodeStateFailed:
		return "FAILED"
	case NodeStateDegraded:
		return "DEGRADED"
	case NodeStateLeaving:
		return "LEAVING"
	case NodeStateUnknown:
		return "UNKNOWN"
	default:
		return "INVALID"
	}
}

// NodeHealth tracks the observed health of a peer node.
type NodeHealth struct {
	NodeID           NodeID
	State            NodeState
	LastHeartbeat    time.Time
	MissedHeartbeats int
	LastLatency      time.Duration
}

// IsHealthy returns true if the node can participate in operations.
func (nh *NodeHealth) IsHealthy() bool {
	return nh.State == NodeStateActive
}

// CanServeReads returns true if the node can serve read requests. Even
// degraded nodes can serve reads.
func (nh *NodeHealth) CanServeReads() bool {
	return nh.State == NodeStateActive || nh.State == NodeStateDegraded
}

// CanServeWrites returns true if the node can accept write requests. Only
// active nodes can accept writes.
func (nh *NodeHealth) CanServeWrites() bool {
	return nh.State == NodeStateActive
}

// PeerStatus represents the membership status of a peer node.
type PeerStatus int

const (
	PeerStatusUnknown PeerStatus = iota
	PeerStatusActive
	PeerStatusSuspect
	PeerStatusFailed
)

// String returns the human-readable name of the status.
func (s PeerStatus) String() string {
	switch s {
	case PeerStatusActive:
		return "ACTIVE"
	case PeerStatusSuspect:
		return "SUSPECT"
	case PeerStatusFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// PeerInfo represents information about a peer known to the membership
// layer.
type PeerInfo struct {
	ID       NodeID
	Addr     string
	Status   PeerStatus
	LastSeen time.Time
}
