package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"GoQuorum/internal/cluster"
	"GoQuorum/internal/common"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

// InternalAPI implements the internal node-to-node service
type InternalAPI struct {
	storage      *storage.Storage
	membership   *cluster.MembershipManager
	merkleRootFn func() []byte // returns current Merkle root; set after coordinator starts
	gossip       *cluster.Gossip
}

// NewInternalAPI creates a new internal API service
func NewInternalAPI(store *storage.Storage, membership *cluster.MembershipManager) *InternalAPI {
	return &InternalAPI{
		storage:    store,
		membership: membership,
	}
}

// SetMerkleRootFn wires the Merkle-root provider from the coordinator.
func (i *InternalAPI) SetMerkleRootFn(fn func() []byte) {
	i.merkleRootFn = fn
}

// SetGossip attaches a Gossip instance to the InternalAPI.
func (i *InternalAPI) SetGossip(g *cluster.Gossip) {
	i.gossip = g
}

// handleGossipExchange decodes incoming gossip state, merges it, and replies with
// this node's current gossip state.
func (i *InternalAPI) handleGossipExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if i.gossip == nil {
		http.Error(w, "gossip not enabled", http.StatusServiceUnavailable)
		return
	}

	var incoming map[common.NodeID]*cluster.NodeEntry
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	i.gossip.Merge(incoming)

	state := i.gossip.GetState()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(state); err != nil {
		// Response already committed — just log.
		_ = err
	}
}

// Replicate handles replication requests from coordinator
func (i *InternalAPI) Replicate(ctx context.Context, req *ReplicateReq) (*ReplicateResp, error) {
	if i.storage == nil {
		return &ReplicateResp{Success: false, Error: "storage not initialized"}, nil
	}

	// Convert to internal sibling
	sibling := storage.Sibling{
		Value:     req.Sibling.Value,
		VClock:    entriesToVClock(req.Sibling.Context),
		Timestamp: req.Sibling.Timestamp,
		Tombstone: req.Sibling.Tombstone,
	}

	// Create sibling set
	siblingSet := &storage.SiblingSet{
		Siblings: []storage.Sibling{sibling},
	}

	// Write to local storage
	if err := i.storage.Put(req.Key, siblingSet); err != nil {
		return &ReplicateResp{Success: false, Error: err.Error()}, nil
	}

	return &ReplicateResp{Success: true}, nil
}

// Read handles read requests from coordinator
func (i *InternalAPI) Read(ctx context.Context, req *InternalReadReq) (*InternalReadResp, error) {
	if i.storage == nil {
		return &InternalReadResp{Found: false}, nil
	}

	// Read from local storage
	siblingSet, err := i.storage.Get(req.Key)
	if err != nil {
		return &InternalReadResp{Found: false}, nil
	}

	if siblingSet == nil || len(siblingSet.Siblings) == 0 {
		return &InternalReadResp{Found: false}, nil
	}

	// Convert to response format
	resp := &InternalReadResp{
		Found:    true,
		Siblings: make([]SiblingData, 0, len(siblingSet.Siblings)),
	}

	for _, sib := range siblingSet.Siblings {
		resp.Siblings = append(resp.Siblings, SiblingData{
			Value:     sib.Value,
			Context:   vclockToEntries(sib.VClock),
			Tombstone: sib.Tombstone,
			Timestamp: sib.Timestamp,
		})
	}

	return resp, nil
}

// Heartbeat handles heartbeat requests for failure detection
func (i *InternalAPI) Heartbeat(ctx context.Context, req *HeartbeatReq) (*HeartbeatResp, error) {
	resp := &HeartbeatResp{
		ResponderID: string(i.storage.LocalNodeID()),
		Timestamp:   time.Now().Unix(),
		Status:      NodeStatusActive,
		Peers:       []PeerStatusData{},
	}

	// Get peer status from membership
	if i.membership != nil {
		peers := i.membership.GetPeers()
		for _, peer := range peers {
			resp.Peers = append(resp.Peers, PeerStatusData{
				NodeID:   string(peer.ID),
				Status:   peerStatusToNodeStatus(peer.Status),
				LastSeen: peer.LastSeen.UnixNano(),
			})
		}
	}

	return resp, nil
}

// NotifyLeaving handles a node's graceful-leave notification
func (i *InternalAPI) NotifyLeaving(ctx context.Context, req *NotifyLeavingReq) (*NotifyLeavingResp, error) {
	if i.membership != nil && req.NodeID != "" {
		i.membership.UpdatePeerStatus(common.NodeID(req.NodeID), cluster.NodeStatusLeaving)
	}
	return &NotifyLeavingResp{Acknowledged: true}, nil
}

// GetMerkleRoot returns the Merkle tree root hash
func (i *InternalAPI) GetMerkleRoot(ctx context.Context, req *GetMerkleRootReq) (*GetMerkleRootResp, error) {
	if i.merkleRootFn == nil {
		return &GetMerkleRootResp{MerkleRoot: make([]byte, 32)}, nil
	}
	return &GetMerkleRootResp{MerkleRoot: i.merkleRootFn()}, nil
}

// Request/Response types for internal API

type ReplicateReq struct {
	Key           []byte      `json:"key"`
	Sibling       SiblingData `json:"sibling"`
	CoordinatorID string      `json:"coordinator_id,omitempty"`
	RequestID     int64       `json:"request_id,omitempty"`
}

type ReplicateResp struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type SiblingData struct {
	Value     []byte             `json:"value"`
	Context   []ContextEntryData `json:"context"`
	Tombstone bool               `json:"tombstone"`
	Timestamp int64              `json:"timestamp"`
}

type ContextEntryData struct {
	NodeID  string `json:"node_id"`
	Counter uint64 `json:"counter"`
}

type InternalReadReq struct {
	Key           []byte `json:"key"`
	CoordinatorID string `json:"coordinator_id,omitempty"`
}

type InternalReadResp struct {
	Siblings []SiblingData `json:"siblings"`
	Found    bool          `json:"found"`
}

type HeartbeatReq struct {
	SenderID  string         `json:"sender_id"`
	Timestamp int64          `json:"timestamp"`
	Version   string         `json:"version,omitempty"`
	Status    NodeStatusType `json:"status,omitempty"`
}

type HeartbeatResp struct {
	ResponderID string           `json:"responder_id"`
	Timestamp   int64            `json:"timestamp"`
	Status      NodeStatusType   `json:"status,omitempty"`
	Peers       []PeerStatusData `json:"peers,omitempty"`
}

type NodeStatusType int

const (
	NodeStatusUnknown NodeStatusType = iota
	NodeStatusJoining
	NodeStatusActive
	NodeStatusLeaving
)

type PeerStatusData struct {
	NodeID   string         `json:"node_id"`
	Status   NodeStatusType `json:"status"`
	LastSeen int64          `json:"last_seen"`
}

type GetMerkleRootReq struct {
	SenderID string `json:"sender_id,omitempty"`
}

type GetMerkleRootResp struct {
	MerkleRoot []byte `json:"merkle_root"`
}

type NotifyLeavingReq struct {
	NodeID string `json:"node_id"`
}

type NotifyLeavingResp struct {
	Acknowledged bool `json:"acknowledged"`
}

// Helper functions

func entriesToVClock(entries []ContextEntryData) vclock.VectorClock {
	vc := vclock.NewVectorClock()
	for _, entry := range entries {
		vc.SetString(entry.NodeID, entry.Counter)
	}
	return vc
}

func vclockToEntries(vc vclock.VectorClock) []ContextEntryData {
	entries := make([]ContextEntryData, 0)
	for nodeID, counter := range vc.Entries() {
		entries = append(entries, ContextEntryData{
			NodeID:  nodeID,
			Counter: counter,
		})
	}
	return entries
}

func peerStatusToNodeStatus(status common.PeerStatus) NodeStatusType {
	switch status {
	case common.PeerStatusActive:
		return NodeStatusActive
	case common.PeerStatusSuspect:
		return NodeStatusActive // Suspect is still considered active
	case common.PeerStatusFailed:
		return NodeStatusUnknown
	default:
		return NodeStatusUnknown
	}
}
