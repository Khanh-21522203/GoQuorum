package common

import "time"

// NodeState represents the health state of a node (Section 3.3)
type NodeState int

const (
	NodeStateActive   NodeState = iota // Node is healthy and operational
	NodeStateFailed                    // Node is unreachable (Section 3.3)
	NodeStateDegraded                  // Node is slow or degraded (Section 6, 8.2)
	NodeStateLeaving                   // Node is gracefully shutting down (Section 4.1)
	NodeStateUnknown                   // Initial state or unknown
)

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

// NodeHealth tracks peer health (Section 3.3, 9)
type NodeHealth struct {
	NodeID           NodeID
	State            NodeState
	LastHeartbeat    time.Time
	MissedHeartbeats int
	LastLatency      time.Duration
}

// IsHealthy returns true if node can participate in operations
func (nh *NodeHealth) IsHealthy() bool {
	return nh.State == NodeStateActive
}

// CanServeReads returns true if node can serve read requests
func (nh *NodeHealth) CanServeReads() bool {
	// Even degraded nodes can serve reads (Section 8.2)
	return nh.State == NodeStateActive || nh.State == NodeStateDegraded
}

// CanServeWrites returns true if node can accept write requests
func (nh *NodeHealth) CanServeWrites() bool {
	// Only active nodes can accept writes
	return nh.State == NodeStateActive
}

// PeerStatus represents the status of a peer node
type PeerStatus int

const (
	PeerStatusUnknown PeerStatus = iota
	PeerStatusActive
	PeerStatusSuspect
	PeerStatusFailed
)

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

// PeerInfo represents information about a peer
type PeerInfo struct {
	ID       NodeID
	Addr     string
	Status   PeerStatus
	LastSeen time.Time
}
