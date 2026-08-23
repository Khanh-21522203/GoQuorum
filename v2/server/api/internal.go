package api

import (
	"context"
	"time"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/coordinator"
	"goquorum.io/v2/engine/membership"
)

// InternalAPI implements the node-to-node service invoked by peer
// coordinators: replication, direct local reads, heartbeats, graceful-leave
// notification, and Merkle-root exchange for anti-entropy.
//
// (v1: internal/server/internal_api.go InternalAPI)
type InternalAPI struct {
	storage    adapter.Storage
	membership *membership.MembershipManager

	// coordinator backs GetMerkleRoot. v1 wired an equivalent
	// merkleRootFn func() []byte in after the coordinator started
	// (SetMerkleRootFn), to avoid a construction-order dependency: the
	// v1 InternalAPI was built before its coordinator existed. v2's
	// composition root (server/app) builds the coordinator first, so
	// InternalAPI can simply take it as a constructor dependency.
	coordinator *coordinator.Coordinator
}

// NewInternalAPI creates an internal API service over the given storage
// port, membership view, and coordinator (consulted only for its Merkle
// root).
//
// (v1: internal/server/internal_api.go NewInternalAPI)
func NewInternalAPI(store adapter.Storage, mm *membership.MembershipManager, coord *coordinator.Coordinator) *InternalAPI {
	return &InternalAPI{storage: store, membership: mm, coordinator: coord}
}

// ReplicateReq carries a single sibling to write into the receiving node's
// local storage.
//
// (v1: internal/server/internal_api.go ReplicateReq; v2 reuses
// storage.Sibling directly instead of v1's hand-rolled SiblingData/
// ContextEntryData, since vclock.VectorClock now marshals to JSON on its
// own.)
type ReplicateReq struct {
	Key           []byte
	Sibling       adapter.Sibling
	CoordinatorID node.NodeID
	RequestID     int64
}

// ReplicateResp reports the outcome of a Replicate call.
//
// (v1: internal/server/internal_api.go ReplicateResp)
type ReplicateResp struct {
	Success bool
	Error   string // empty if success.
}

// InternalReadReq requests a key's sibling set from local storage,
// bypassing quorum.
//
// (v1: internal/server/internal_api.go InternalReadReq)
type InternalReadReq struct {
	Key           []byte
	CoordinatorID node.NodeID
}

// InternalReadResp is the local sibling set for a key.
//
// (v1: internal/server/internal_api.go InternalReadResp)
type InternalReadResp struct {
	Siblings []adapter.Sibling
	Found    bool
}

// HeartbeatReq is a liveness probe from a peer coordinator.
//
// (v1: internal/server/internal_api.go HeartbeatReq; v2 reuses
// membership.NodeStatus instead of v1's hand-rolled NodeStatusType.)
type HeartbeatReq struct {
	SenderID  node.NodeID
	Timestamp time.Time
	Version   string
	Status    membership.NodeStatus
}

// HeartbeatResp is the receiving node's liveness reply, including its view
// of the cluster's peers.
//
// (v1: internal/server/internal_api.go HeartbeatResp; v2 reuses
// node.PeerInfo instead of v1's hand-rolled PeerStatusData, matching
// membership.MembershipManager.GetPeers()'s return type.)
type HeartbeatResp struct {
	ResponderID node.NodeID
	Timestamp   time.Time
	Status      membership.NodeStatus
	Peers       []node.PeerInfo
}

// GetMerkleRootReq requests the current anti-entropy Merkle root.
//
// (v1: internal/server/internal_api.go GetMerkleRootReq)
type GetMerkleRootReq struct {
	SenderID node.NodeID
}

// GetMerkleRootResp carries the requested Merkle root.
//
// (v1: internal/server/internal_api.go GetMerkleRootResp)
type GetMerkleRootResp struct {
	MerkleRoot []byte
}

// NotifyLeavingReq informs the receiving node that the sender is leaving
// the cluster gracefully.
//
// (v1: internal/server/internal_api.go NotifyLeavingReq)
type NotifyLeavingReq struct {
	NodeID node.NodeID
}

// NotifyLeavingResp acknowledges a NotifyLeaving call.
//
// (v1: internal/server/internal_api.go NotifyLeavingResp)
type NotifyLeavingResp struct {
	Acknowledged bool
}

// Replicate writes req.Sibling into local storage under req.Key.
//
// TODO(v2): build a storage.SiblingSet around req.Sibling and call
// i.storage.Put(req.Key, siblingSet) (v1:
// internal/server/internal_api.go InternalAPI.Replicate).
func (i *InternalAPI) Replicate(ctx context.Context, req *ReplicateReq) (*ReplicateResp, error) {
	return nil, contracts.ErrNotImplemented
}

// Read returns the local sibling set for req.Key.
//
// TODO(v2): call i.storage.Get(req.Key) and translate
// quorumerr.ErrKeyNotFound into Found: false rather than an error (v1:
// internal/server/internal_api.go InternalAPI.Read).
func (i *InternalAPI) Read(ctx context.Context, req *InternalReadReq) (*InternalReadResp, error) {
	return nil, contracts.ErrNotImplemented
}

// Heartbeat responds to a liveness probe with this node's status and its
// view of the cluster's peers.
//
// TODO(v2): populate ResponderID from i.storage.LocalNodeID(), Status from
// i.membership.GetLocalStatus(), and Peers from i.membership.GetPeers()
// (v1: internal/server/internal_api.go InternalAPI.Heartbeat).
func (i *InternalAPI) Heartbeat(ctx context.Context, req *HeartbeatReq) (*HeartbeatResp, error) {
	return nil, contracts.ErrNotImplemented
}

// NotifyLeaving marks req.NodeID as leaving in the local membership view.
//
// TODO(v2): call i.membership.UpdatePeerStatus(req.NodeID,
// membership.NodeStatusLeaving) (v1: internal/server/internal_api.go
// InternalAPI.NotifyLeaving).
func (i *InternalAPI) NotifyLeaving(ctx context.Context, req *NotifyLeavingReq) (*NotifyLeavingResp, error) {
	return nil, contracts.ErrNotImplemented
}

// GetMerkleRoot returns the coordinator's current anti-entropy Merkle
// root.
//
// TODO(v2): return &GetMerkleRootResp{MerkleRoot: i.coordinator.GetMerkleRoot()}
// (v1: internal/server/internal_api.go InternalAPI.GetMerkleRoot, which
// read a merkleRootFn wired in after coordinator.Start()).
func (i *InternalAPI) GetMerkleRoot(ctx context.Context, req *GetMerkleRootReq) (*GetMerkleRootResp, error) {
	return nil, contracts.ErrNotImplemented
}
