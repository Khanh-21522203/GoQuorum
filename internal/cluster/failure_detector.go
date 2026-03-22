package cluster

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"context"
	"fmt"
	"sync"
	"time"
)

// FailureDetector monitors peer health via heartbeats (Section 3.3, 9)
type FailureDetector struct {
	config config.FailureDetectorConfig
	peers  map[common.NodeID]*common.NodeHealth
	mu     sync.RWMutex

	membership *MembershipManager

	rpc     RPCClient
	metrics *FailureDetectorMetrics

	// Optional callbacks — set after construction, before Start().
	// OnNodeRecovery is called once when a node transitions FAILED → ACTIVE.
	OnNodeRecovery func(nodeID common.NodeID)
	// OnNodeFailed is called once when a node is first confirmed FAILED.
	OnNodeFailed func(nodeID common.NodeID)

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func NewFailureDetector(
	config config.FailureDetectorConfig,
	membership *MembershipManager,
	rpc RPCClient) *FailureDetector {

	return &FailureDetector{
		config:     config,
		peers:      make(map[common.NodeID]*common.NodeHealth),
		membership: membership,
		rpc:        rpc,
		metrics:    NewFailureDetectorMetrics(),
		stopCh:     make(chan struct{}),
	}
}

// Start begins heartbeat monitoring
func (fd *FailureDetector) Start(peerIDs []common.NodeID) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	// Initialize peer health
	for _, peerID := range peerIDs {
		fd.peers[peerID] = &common.NodeHealth{
			NodeID:           peerID,
			State:            common.NodeStateUnknown,
			LastHeartbeat:    time.Time{},
			MissedHeartbeats: 0,
		}
	}

	// Start heartbeat loop
	fd.wg.Add(1)
	go fd.heartbeatLoop()
}

// Stop stops failure detector
func (fd *FailureDetector) Stop() {
	close(fd.stopCh)
	fd.wg.Wait()
}

// heartbeatLoop sends periodic heartbeats (Section 3.3)
func (fd *FailureDetector) heartbeatLoop() {
	defer fd.wg.Done()

	ticker := time.NewTicker(fd.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			fd.sendHeartbeats()
		case <-fd.stopCh:
			return
		}
	}
}

// sendHeartbeats sends heartbeat to all peers
func (fd *FailureDetector) sendHeartbeats() {
	fd.mu.RLock()
	peers := make(map[common.NodeID]*common.NodeHealth)
	for id, health := range fd.peers {
		peers[id] = health
	}
	fd.mu.RUnlock()

	for nodeID := range peers {
		go fd.sendHeartbeat(nodeID)
	}
}

// sendHeartbeat sends heartbeat to single peer
func (fd *FailureDetector) sendHeartbeat(nodeID common.NodeID) {
	ctx, cancel := context.WithTimeout(context.Background(), fd.config.HeartbeatTimeout)
	defer cancel()

	prevStatus := fd.membership.GetPeerStatus(nodeID)

	start := time.Now()
	err := fd.rpc.Heartbeat(ctx, nodeID)
	latency := time.Since(start)

	if err == nil {
		fd.membership.RecordHeartbeatSuccess(nodeID, latency)
		fd.metrics.HeartbeatSuccess.Inc()

		// Partition healed: node was FAILED and is now reachable again.
		if prevStatus == NodeStatusFailed {
			fd.detectPartitionHealed(nodeID)
			if fd.OnNodeRecovery != nil {
				fd.OnNodeRecovery(nodeID)
			}
		}
	} else {
		fd.membership.RecordHeartbeatFailure(nodeID)
		fd.metrics.HeartbeatFailure.Inc()

		// Partition suspected: node just crossed the failure threshold.
		newStatus := fd.membership.GetPeerStatus(nodeID)
		if prevStatus != NodeStatusFailed && newStatus == NodeStatusFailed {
			fd.detectPartition(nodeID)
			if fd.OnNodeFailed != nil {
				fd.OnNodeFailed(nodeID)
			}
		}
	}
}

// detectPartition logs and metrics a suspected partition when a node becomes
// unreachable while at least one other peer is still healthy.
func (fd *FailureDetector) detectPartition(failedNodeID common.NodeID) {
	activeCount, failedCount := 0, 0
	for _, id := range fd.membership.GetAllPeers() {
		if id == failedNodeID {
			continue
		}
		switch fd.membership.GetPeerStatus(id) {
		case NodeStatusActive:
			activeCount++
		case NodeStatusFailed, NodeStatusSuspect:
			failedCount++
		}
	}

	if activeCount > 0 {
		fd.metrics.PartitionSuspected.Inc()
		fmt.Printf("[PARTITION] Suspected: %s unreachable while %d peer(s) healthy, %d failed\n",
			failedNodeID, activeCount, failedCount+1)
	} else {
		// All peers unreachable — more likely a local network issue than a partition.
		fmt.Printf("[PARTITION] All peers unreachable including %s — possible local isolation\n",
			failedNodeID)
	}
}

// detectPartitionHealed logs and metrics a partition heal when a previously
// failed node becomes reachable again.
func (fd *FailureDetector) detectPartitionHealed(nodeID common.NodeID) {
	fd.metrics.PartitionHealed.Inc()
	fmt.Printf("[PARTITION] Healed: %s is reachable again — triggering post-recovery sync\n", nodeID)
}

// GetHealthyNodes returns list of healthy nodes
func (fd *FailureDetector) GetHealthyNodes() []common.NodeID {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	healthy := make([]common.NodeID, 0)
	for nodeID, health := range fd.peers {
		if health.IsHealthy() {
			healthy = append(healthy, nodeID)
		}
	}
	return healthy
}

// GetNodeState returns state of specific node
func (fd *FailureDetector) GetNodeState(nodeID common.NodeID) common.NodeState {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	if health, exists := fd.peers[nodeID]; exists {
		return health.State
	}
	return common.NodeStateUnknown
}

// IsNodeHealthy returns true if node is healthy
func (fd *FailureDetector) IsNodeHealthy(nodeID common.NodeID) bool {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	if health, exists := fd.peers[nodeID]; exists {
		return health.IsHealthy()
	}
	return false
}

// GetNodeHealth returns health info for node
func (fd *FailureDetector) GetNodeHealth(nodeID common.NodeID) *common.NodeHealth {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	if health, exists := fd.peers[nodeID]; exists {
		// Return copy
		return &common.NodeHealth{
			NodeID:           health.NodeID,
			State:            health.State,
			LastHeartbeat:    health.LastHeartbeat,
			MissedHeartbeats: health.MissedHeartbeats,
			LastLatency:      health.LastLatency,
		}
	}
	return nil
}

// UpdateNodeState manually updates node state (for graceful shutdown)
func (fd *FailureDetector) UpdateNodeState(nodeID common.NodeID, state common.NodeState) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	if health, exists := fd.peers[nodeID]; exists {
		health.State = state
	}
}
