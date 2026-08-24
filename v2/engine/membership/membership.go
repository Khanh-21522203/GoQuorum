package membership

import (
	"time"

	"goquorum.io/v2/contracts/node"
)

// LocalMetadata describes the local node's own membership state.
type LocalMetadata struct {
	NodeID     node.NodeID
	ListenAddr string
	StartTime  time.Time
	Version    string
	Status     NodeStatus
}

// PeerMetadata describes a known peer's membership state as observed by the
// local node.
type PeerMetadata struct {
	NodeID      node.NodeID
	Addr        string // gRPC/replica address.
	HTTPAddr    string // Internal-RPC address.
	Status      NodeStatus
	LastSeen    time.Time
	MissedCount int
	LatencyP99  time.Duration
}

// MembershipManager tracks the local node's status and its view of peer
// nodes, and reports whether a write/read quorum of the cluster is
// reachable.
//
// Metrics reporting is intentionally not a field on this type: engine
// depends only on the standard library and contracts, so instrumentation
// belongs in an infra-level adapter that wraps a MembershipManager rather
// than in the manager itself.
type MembershipManager struct {
	config    Config
	localMeta LocalMetadata
	peers     map[node.NodeID]*PeerMetadata
}

// NewMembershipManager creates a membership manager for the local node
// described by cfg, reporting the given software version. The local node
// starts in NodeStatusJoining; peers are discovered later via AddPeer,
// UpdatePeerStatus, or heartbeat reporting.
func NewMembershipManager(cfg Config, version string) *MembershipManager {
	return &MembershipManager{
		config: cfg,
		localMeta: LocalMetadata{
			NodeID:     cfg.NodeID,
			ListenAddr: cfg.ListenAddr,
			StartTime:  time.Now(),
			Version:    version,
			Status:     NodeStatusJoining,
		},
		peers: make(map[node.NodeID]*PeerMetadata),
	}
}

// GetLocalStatus returns the local node's current status.
func (mm *MembershipManager) GetLocalStatus() NodeStatus {
	return mm.localMeta.Status
}

// SetLocalStatus sets the local node's current status.
func (mm *MembershipManager) SetLocalStatus(status NodeStatus) {
	mm.localMeta.Status = status
}

// GetPeerStatus returns the status of the given peer, or NodeStatusUnknown
// if the peer is not known.
func (mm *MembershipManager) GetPeerStatus(nodeID node.NodeID) NodeStatus {
	if p, ok := mm.peers[nodeID]; ok {
		return p.Status
	}
	return NodeStatusUnknown
}

// UpdatePeerStatus updates the status of the given peer, creating a bare
// peer entry first if nodeID has not been seen before.
func (mm *MembershipManager) UpdatePeerStatus(nodeID node.NodeID, status NodeStatus) {
	p, ok := mm.peers[nodeID]
	if !ok {
		p = &PeerMetadata{NodeID: nodeID}
		mm.peers[nodeID] = p
	}
	p.Status = status
}

// RecordHeartbeatSuccess records a successful heartbeat from nodeID,
// resetting its missed-heartbeat count, recording latency, and marking it
// active. It is a no-op if nodeID is not a known peer.
func (mm *MembershipManager) RecordHeartbeatSuccess(nodeID node.NodeID, latency time.Duration) {
	p, ok := mm.peers[nodeID]
	if !ok {
		return
	}
	p.LastSeen = time.Now()
	p.MissedCount = 0
	p.LatencyP99 = latency
	p.Status = NodeStatusActive
}

// RecordHeartbeatFailure records a missed heartbeat from nodeID. Once the
// configured failure threshold is reached the peer is marked Failed;
// short of that, an Active peer is downgraded to Suspect. It is a no-op if
// nodeID is not a known peer.
func (mm *MembershipManager) RecordHeartbeatFailure(nodeID node.NodeID) {
	p, ok := mm.peers[nodeID]
	if !ok {
		return
	}
	p.MissedCount++

	switch {
	case p.MissedCount >= mm.config.FailureThreshold:
		p.Status = NodeStatusFailed
	case p.Status == NodeStatusActive:
		p.Status = NodeStatusSuspect
	}
}

// GetActivePeers returns the IDs of all peers currently marked active.
func (mm *MembershipManager) GetActivePeers() []node.NodeID {
	active := make([]node.NodeID, 0, len(mm.peers))
	for id, p := range mm.peers {
		if p.Status == NodeStatusActive {
			active = append(active, id)
		}
	}
	return active
}

// GetAllPeers returns the IDs of all known peers, regardless of status.
func (mm *MembershipManager) GetAllPeers() []node.NodeID {
	ids := make([]node.NodeID, 0, len(mm.peers))
	for id := range mm.peers {
		ids = append(ids, id)
	}
	return ids
}

// GetPeerAddr returns the replica address of the given peer, and whether it
// is known.
func (mm *MembershipManager) GetPeerAddr(id node.NodeID) (string, bool) {
	p, ok := mm.peers[id]
	if !ok {
		return "", false
	}
	return p.Addr, true
}

// quorumSize is the minimum number of nodes (out of the full, statically
// configured membership, including the local node) that must be active for
// the cluster to have a majority.
func (mm *MembershipManager) quorumSize() int {
	return (len(mm.config.Members) / 2) + 1
}

// activePeerCount returns the number of peers currently marked active.
func (mm *MembershipManager) activePeerCount() int {
	count := 0
	for _, p := range mm.peers {
		if p.Status == NodeStatusActive {
			count++
		}
	}
	return count
}

// HasQuorum reports whether enough nodes are active, right now, to satisfy
// the configured quorum size. The local node counts toward the total only
// if it is itself Active.
func (mm *MembershipManager) HasQuorum() bool {
	count := mm.activePeerCount()
	if mm.localMeta.Status == NodeStatusActive {
		count++
	}
	return count >= mm.quorumSize()
}

// ActivateIfQuorum atomically checks whether becoming active would still
// leave the cluster at or above quorum, and if so, promotes the local node
// to NodeStatusActive. Unlike HasQuorum, it counts the local node as active
// unconditionally, since it is evaluating the state the cluster would be in
// immediately after activation. It returns whether activation occurred.
func (mm *MembershipManager) ActivateIfQuorum() bool {
	if mm.activePeerCount()+1 < mm.quorumSize() {
		return false
	}
	mm.localMeta.Status = NodeStatusActive
	return true
}

// GetClusterView returns a snapshot of every known node's status, including
// the local node.
func (mm *MembershipManager) GetClusterView() map[node.NodeID]NodeStatus {
	view := make(map[node.NodeID]NodeStatus, len(mm.peers)+1)
	view[mm.localMeta.NodeID] = mm.localMeta.Status
	for id, p := range mm.peers {
		view[id] = p.Status
	}
	return view
}

// ActivePeerCount returns the number of peers currently marked active.
func (mm *MembershipManager) ActivePeerCount() int {
	return mm.activePeerCount()
}

// TotalPeerCount returns the total number of known peers.
func (mm *MembershipManager) TotalPeerCount() int {
	return len(mm.peers)
}

// nodeStatusToPeerStatus maps this package's finer-grained NodeStatus onto
// the coarser contracts/node.PeerStatus enum consumed outside engine.
// Joining and Leaving have no equivalent in PeerStatus, since callers
// outside engine only need to distinguish healthy/suspect/failed peers from
// everything else; both degrade to PeerStatusUnknown.
func nodeStatusToPeerStatus(status NodeStatus) node.PeerStatus {
	switch status {
	case NodeStatusActive:
		return node.PeerStatusActive
	case NodeStatusSuspect:
		return node.PeerStatusSuspect
	case NodeStatusFailed:
		return node.PeerStatusFailed
	default:
		return node.PeerStatusUnknown
	}
}

// GetPeers returns PeerInfo for every known peer, mapping this package's
// NodeStatus onto contracts/node.PeerStatus.
func (mm *MembershipManager) GetPeers() []node.PeerInfo {
	out := make([]node.PeerInfo, 0, len(mm.peers))
	for _, p := range mm.peers {
		out = append(out, node.PeerInfo{
			ID:       p.NodeID,
			Addr:     p.Addr,
			Status:   nodeStatusToPeerStatus(p.Status),
			LastSeen: p.LastSeen,
		})
	}
	return out
}

// GetAllNodes returns the IDs of every known node, including the local
// node.
func (mm *MembershipManager) GetAllNodes() []node.NodeID {
	out := make([]node.NodeID, 0, len(mm.peers)+1)
	out = append(out, mm.localMeta.NodeID)
	for id := range mm.peers {
		out = append(out, id)
	}
	return out
}

// GetAddress returns the replica address for nodeID, or the local node's
// own ListenAddr if nodeID is the local node.
func (mm *MembershipManager) GetAddress(nodeID node.NodeID) string {
	if nodeID == mm.localMeta.NodeID {
		return mm.localMeta.ListenAddr
	}
	if p, ok := mm.peers[nodeID]; ok {
		return p.Addr
	}
	return ""
}

// GetHTTPAddress returns the internal-RPC address for nodeID, or the local
// node's own ListenAddr if nodeID is the local node. LocalMetadata carries
// only one address for the local node, so it doubles as both the replica
// and internal-RPC address for the local case.
func (mm *MembershipManager) GetHTTPAddress(nodeID node.NodeID) string {
	if nodeID == mm.localMeta.NodeID {
		return mm.localMeta.ListenAddr
	}
	if p, ok := mm.peers[nodeID]; ok {
		return p.HTTPAddr
	}
	return ""
}

// LocalNodeID returns the local node's ID.
func (mm *MembershipManager) LocalNodeID() node.NodeID {
	return mm.localMeta.NodeID
}

// AddPeer registers a peer discovered dynamically (e.g. via gossip or a
// join request) that was not present in the static configuration. It is a
// no-op if nodeID is already known.
func (mm *MembershipManager) AddPeer(nodeID node.NodeID, grpcAddr, httpAddr string) {
	if _, exists := mm.peers[nodeID]; exists {
		return
	}
	mm.peers[nodeID] = &PeerMetadata{
		NodeID:   nodeID,
		Addr:     grpcAddr,
		HTTPAddr: httpAddr,
		Status:   NodeStatusJoining,
	}
}

// RemovePeer removes a peer that has left the cluster.
func (mm *MembershipManager) RemovePeer(nodeID node.NodeID) {
	delete(mm.peers, nodeID)
}
