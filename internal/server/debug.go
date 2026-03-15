package server

import (
	"encoding/json"
	"net/http"
)

// ClusterDebugInfo represents cluster debug information
type ClusterDebugInfo struct {
	NodeID string          `json:"node_id"`
	Status string          `json:"status"`
	Peers  []PeerDebugInfo `json:"peers"`
}

// PeerDebugInfo represents peer debug information
type PeerDebugInfo struct {
	NodeID            string `json:"node_id"`
	Addr              string `json:"addr"`
	Status            string `json:"status"`
	LastSeen          string `json:"last_seen"`
	LatencyP99Ms      int64  `json:"latency_p99_ms"`
	HeartbeatFailures int    `json:"heartbeat_failures"`
}

// RingDebugInfo represents hash ring debug information
type RingDebugInfo struct {
	VNodesPerNode int            `json:"vnodes_per_node"`
	TotalVNodes   int            `json:"total_vnodes"`
	Nodes         []NodeRingInfo `json:"nodes"`
}

// NodeRingInfo represents a node's hash ring information
type NodeRingInfo struct {
	NodeID      string  `json:"node_id"`
	VNodes      int     `json:"vnodes"`
	KeyRangePct float64 `json:"key_range_pct"`
}

// handleDebugCluster handles GET /debug/cluster
func (s *Server) handleDebugCluster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info := ClusterDebugInfo{
		NodeID: s.nodeID,
		Status: "ACTIVE",
		Peers:  []PeerDebugInfo{},
	}

	if s.membership != nil {
		peers := s.membership.GetPeers()
		for _, peer := range peers {
			peerInfo := PeerDebugInfo{
				NodeID:   string(peer.ID),
				Addr:     peer.Addr,
				Status:   peer.Status.String(),
				LastSeen: peer.LastSeen.Format("2006-01-02T15:04:05Z"),
			}
			info.Peers = append(info.Peers, peerInfo)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleDebugRing handles GET /debug/ring
func (s *Server) handleDebugRing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	vnodesPerNode := 256
	if s.ring != nil {
		vnodesPerNode = s.ring.VNodeCount()
	}

	info := RingDebugInfo{
		VNodesPerNode: vnodesPerNode,
		TotalVNodes:   0,
		Nodes:         []NodeRingInfo{},
	}

	if s.membership != nil {
		nodes := s.membership.GetAllNodes()
		info.TotalVNodes = len(nodes) * vnodesPerNode

		for _, nodeID := range nodes {
			nodeInfo := NodeRingInfo{
				NodeID:      string(nodeID),
				VNodes:      vnodesPerNode,
				KeyRangePct: 100.0 / float64(len(nodes)),
			}
			info.Nodes = append(info.Nodes, nodeInfo)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}
