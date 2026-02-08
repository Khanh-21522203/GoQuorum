package cluster

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"fmt"
	"sync"
	"time"
)

// MembershipManager manages cluster membership (Section 2)
type MembershipManager struct {
	config config.ClusterConfig

	// Local metadata (Section 3.3)
	localMeta LocalMetadata

	// Peer metadata (Section 3.3)
	peers map[common.NodeID]*PeerMetadata
	mu    sync.RWMutex

	metrics *MembershipMetrics
}

// LocalMetadata tracks local node information (Section 3.3)
type LocalMetadata struct {
	NodeID     common.NodeID
	ListenAddr string
	StartTime  time.Time
	Version    string
	Status     NodeStatus
}

// PeerMetadata tracks peer information (Section 3.3)
type PeerMetadata struct {
	NodeID      common.NodeID
	Addr        string
	Status      NodeStatus
	LastSeen    time.Time
	MissedCount int
	LatencyP99  time.Duration
}

// NodeStatus represents node operational status (Section 3.3)
type NodeStatus int

const (
	NodeStatusUnknown NodeStatus = iota
	NodeStatusJoining            // Node is joining cluster
	NodeStatusActive             // Node is operational
	NodeStatusSuspect            // Node suspected failed
	NodeStatusFailed             // Node confirmed failed
	NodeStatusLeaving            // Node gracefully leaving
)

func (s NodeStatus) String() string {
	switch s {
	case NodeStatusUnknown:
		return "UNKNOWN"
	case NodeStatusJoining:
		return "JOINING"
	case NodeStatusActive:
		return "ACTIVE"
	case NodeStatusSuspect:
		return "SUSPECT"
	case NodeStatusFailed:
		return "FAILED"
	case NodeStatusLeaving:
		return "LEAVING"
	default:
		return "INVALID"
	}
}

func NewMembershipManager(cfg config.ClusterConfig, version string) *MembershipManager {
	mm := &MembershipManager{
		config: cfg,
		localMeta: LocalMetadata{
			NodeID:     cfg.NodeID,
			ListenAddr: cfg.ListenAddr,
			StartTime:  time.Now(),
			Version:    version,
			Status:     NodeStatusJoining, // Start in JOINING (Section 5.1)
		},
		peers:   make(map[common.NodeID]*PeerMetadata),
		metrics: NewMembershipMetrics(),
	}

	// Initialize peer metadata (Section 5.1 Step 2)
	for _, member := range cfg.GetPeers() {
		mm.peers[member.ID] = &PeerMetadata{
			NodeID:      member.ID,
			Addr:        member.Addr,
			Status:      NodeStatusUnknown,
			LastSeen:    time.Time{},
			MissedCount: 0,
		}
	}

	return mm
}

// GetLocalStatus returns local node status
func (mm *MembershipManager) GetLocalStatus() NodeStatus {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.localMeta.Status
}

// SetLocalStatus updates local node status
func (mm *MembershipManager) SetLocalStatus(status NodeStatus) {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	oldStatus := mm.localMeta.Status
	mm.localMeta.Status = status

	fmt.Printf("Node status changed: %s → %s\n", oldStatus, status)
}

// GetPeerStatus returns peer status
func (mm *MembershipManager) GetPeerStatus(nodeID common.NodeID) NodeStatus {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	if peer, exists := mm.peers[nodeID]; exists {
		return peer.Status
	}
	return NodeStatusUnknown
}

// UpdatePeerStatus updates peer status (Section 4.3)
func (mm *MembershipManager) UpdatePeerStatus(nodeID common.NodeID, status NodeStatus) {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	peer, exists := mm.peers[nodeID]
	if !exists {
		return
	}

	oldStatus := peer.Status
	peer.Status = status

	// Track state transitions (Section 9.1)
	if oldStatus != status {
		mm.metrics.PeerStateChanges.Inc()
		fmt.Printf("Peer %s status changed: %s → %s\n", nodeID, oldStatus, status)
	}

	// Update metrics (Section 9.1)
	switch status {
	case NodeStatusActive:
		mm.metrics.PeersActive.Inc()
		if oldStatus == NodeStatusFailed {
			mm.metrics.PeersRecovered.Inc()
		}
	case NodeStatusFailed:
		mm.metrics.PeersFailed.Inc()
	}
}

// RecordHeartbeatSuccess records successful heartbeat (Section 4.3)
func (mm *MembershipManager) RecordHeartbeatSuccess(nodeID common.NodeID, latency time.Duration) {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	peer, exists := mm.peers[nodeID]
	if !exists {
		return
	}

	peer.LastSeen = time.Now()
	peer.MissedCount = 0
	peer.LatencyP99 = latency // Simplified: just store latest

	// Transition to ACTIVE if was FAILED (Section 4.4)
	if peer.Status == NodeStatusFailed || peer.Status == NodeStatusUnknown {
		oldStatus := peer.Status
		peer.Status = NodeStatusActive

		if oldStatus == NodeStatusFailed {
			mm.metrics.PeersRecovered.Inc()
			fmt.Printf("Peer %s recovered after failure\n", nodeID)
		}
	}
}

// RecordHeartbeatFailure records failed heartbeat (Section 4.3)
func (mm *MembershipManager) RecordHeartbeatFailure(nodeID common.NodeID) {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	peer, exists := mm.peers[nodeID]
	if !exists {
		return
	}

	peer.MissedCount++

	// Check failure threshold (Section 4.3)
	if peer.MissedCount >= mm.config.FailureThreshold {
		if peer.Status != NodeStatusFailed {
			peer.Status = NodeStatusFailed
			mm.metrics.PeersFailed.Inc()
			fmt.Printf("Peer %s marked FAILED (missed %d heartbeats)\n",
				nodeID, peer.MissedCount)
		}
	} else if peer.MissedCount > 0 {
		// Transition to SUSPECT (Section 4.4)
		if peer.Status == NodeStatusActive {
			peer.Status = NodeStatusSuspect
			fmt.Printf("Peer %s marked SUSPECT (missed %d heartbeats)\n",
				nodeID, peer.MissedCount)
		}
	}
}

// GetActivePeers returns list of active peers
func (mm *MembershipManager) GetActivePeers() []common.NodeID {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	active := make([]common.NodeID, 0)
	for nodeID, peer := range mm.peers {
		if peer.Status == NodeStatusActive {
			active = append(active, nodeID)
		}
	}
	return active
}

// GetAllPeers returns all configured peers
func (mm *MembershipManager) GetAllPeers() []common.NodeID {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	peers := make([]common.NodeID, 0, len(mm.peers))
	for nodeID := range mm.peers {
		peers = append(peers, nodeID)
	}
	return peers
}

// GetPeerAddr returns address for peer
func (mm *MembershipManager) GetPeerAddr(nodeID common.NodeID) (string, bool) {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	if peer, exists := mm.peers[nodeID]; exists {
		return peer.Addr, true
	}
	return "", false
}

// HasQuorum checks if we have connectivity to quorum (Section 5.1)
func (mm *MembershipManager) HasQuorum() bool {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	activeCount := 1 // Include self
	for _, peer := range mm.peers {
		if peer.Status == NodeStatusActive {
			activeCount++
		}
	}

	quorumSize := mm.config.QuorumSize()
	return activeCount >= quorumSize
}

// GetClusterView returns current cluster view (Section 9.2)
func (mm *MembershipManager) GetClusterView() map[common.NodeID]NodeStatus {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	view := make(map[common.NodeID]NodeStatus)

	// Include self
	view[mm.localMeta.NodeID] = mm.localMeta.Status

	// Include peers
	for nodeID, peer := range mm.peers {
		view[nodeID] = peer.Status
	}

	return view
}

// ActivePeerCount returns count of active peers
func (mm *MembershipManager) ActivePeerCount() int {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	count := 0
	for _, peer := range mm.peers {
		if peer.Status == NodeStatusActive {
			count++
		}
	}
	return count
}

// TotalPeerCount returns total count of configured peers
func (mm *MembershipManager) TotalPeerCount() int {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return len(mm.peers)
}

// GetPeers returns all peers with their info
func (mm *MembershipManager) GetPeers() []common.PeerInfo {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	peers := make([]common.PeerInfo, 0, len(mm.peers))
	for _, peer := range mm.peers {
		peers = append(peers, common.PeerInfo{
			ID:       peer.NodeID,
			Addr:     peer.Addr,
			Status:   nodeStatusToPeerStatus(peer.Status),
			LastSeen: peer.LastSeen,
		})
	}
	return peers
}

// GetAllNodes returns all nodes including self
func (mm *MembershipManager) GetAllNodes() []common.NodeID {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	nodes := make([]common.NodeID, 0, len(mm.peers)+1)
	nodes = append(nodes, mm.localMeta.NodeID)
	for nodeID := range mm.peers {
		nodes = append(nodes, nodeID)
	}
	return nodes
}

// GetAddress returns address for a node
func (mm *MembershipManager) GetAddress(nodeID common.NodeID) string {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	// Check if it's self
	if nodeID == mm.localMeta.NodeID {
		return mm.localMeta.ListenAddr
	}

	if peer, exists := mm.peers[nodeID]; exists {
		return peer.Addr
	}
	return ""
}

// LocalNodeID returns the local node ID
func (mm *MembershipManager) LocalNodeID() common.NodeID {
	return mm.localMeta.NodeID
}

// nodeStatusToPeerStatus converts NodeStatus to common.PeerStatus
func nodeStatusToPeerStatus(status NodeStatus) common.PeerStatus {
	switch status {
	case NodeStatusActive:
		return common.PeerStatusActive
	case NodeStatusSuspect:
		return common.PeerStatusSuspect
	case NodeStatusFailed:
		return common.PeerStatusFailed
	default:
		return common.PeerStatusUnknown
	}
}
