package server

import (
	"context"
	"time"

	"GoQuorum/internal/cluster"
	"GoQuorum/internal/common"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

// InternalAPI implements the internal node-to-node gRPC service
type InternalAPI struct {
	storage    *storage.Storage
	membership *cluster.MembershipManager
}

// NewInternalAPI creates a new internal API service
func NewInternalAPI(store *storage.Storage, membership *cluster.MembershipManager) *InternalAPI {
	return &InternalAPI{
		storage:    store,
		membership: membership,
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
		Timestamp:   time.Now().UnixNano(),
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

// GetMerkleRoot returns the Merkle tree root hash
func (i *InternalAPI) GetMerkleRoot(ctx context.Context, req *GetMerkleRootReq) (*GetMerkleRootResp, error) {
	// This would be implemented when anti-entropy is fully integrated
	return &GetMerkleRootResp{
		MerkleRoot: []byte{},
	}, nil
}

// Request/Response types for internal API

type ReplicateReq struct {
	Key           []byte
	Sibling       SiblingData
	CoordinatorID string
	RequestID     int64
}

type ReplicateResp struct {
	Success bool
	Error   string
}

type SiblingData struct {
	Value     []byte
	Context   []ContextEntryData
	Tombstone bool
	Timestamp int64
}

type ContextEntryData struct {
	NodeID  string
	Counter uint64
}

type InternalReadReq struct {
	Key           []byte
	CoordinatorID string
}

type InternalReadResp struct {
	Siblings []SiblingData
	Found    bool
}

type HeartbeatReq struct {
	SenderID  string
	Timestamp int64
	Version   string
	Status    NodeStatusType
}

type HeartbeatResp struct {
	ResponderID string
	Timestamp   int64
	Status      NodeStatusType
	Peers       []PeerStatusData
}

type NodeStatusType int

const (
	NodeStatusUnknown NodeStatusType = iota
	NodeStatusJoining
	NodeStatusActive
	NodeStatusLeaving
)

type PeerStatusData struct {
	NodeID   string
	Status   NodeStatusType
	LastSeen int64
}

type GetMerkleRootReq struct {
	SenderID string
}

type GetMerkleRootResp struct {
	MerkleRoot []byte
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
