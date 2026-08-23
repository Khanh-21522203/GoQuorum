package api

import (
	"time"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter/storage"
	"goquorum.io/v2/engine/membership"
)

// AdminAPI implements the administrative service: health, cluster-view,
// metrics, and per-key introspection, over the storage port and the
// membership view.
//
// (v1: internal/server/admin_api.go AdminAPI)
type AdminAPI struct {
	storage    storage.Storage
	membership *membership.MembershipManager
	nodeID     node.NodeID
	version    string
	startTime  time.Time
}

// NewAdminAPI creates an admin API service over the given storage port and
// membership view, reporting nodeID/version/startTime in Health.
//
// (v1: internal/server/admin_api.go NewAdminAPI)
func NewAdminAPI(store storage.Storage, mm *membership.MembershipManager, nodeID node.NodeID, version string, startTime time.Time) *AdminAPI {
	return &AdminAPI{
		storage:    store,
		membership: mm,
		nodeID:     nodeID,
		version:    version,
		startTime:  startTime,
	}
}

// HealthResult reports node health across its major subsystems.
//
// (v1: internal/server/admin_api.go HealthResult)
type HealthResult struct {
	Status        string
	NodeID        string
	UptimeSeconds int64
	Version       string
	Checks        map[string]CheckInfo
}

// CheckInfo is a single subsystem's health check result.
//
// (v1: internal/server/admin_api.go CheckInfo)
type CheckInfo struct {
	Status      string
	LatencyMs   int64
	Error       string
	PeersActive int
	PeersTotal  int
	FreeBytes   int64
	TotalBytes  int64
}

// ClusterInfoResult reports cluster membership and overall status.
//
// (v1: internal/server/admin_api.go ClusterInfoResult)
type ClusterInfoResult struct {
	NodeID string
	Peers  []PeerInfoResult
	Status string
}

// PeerInfoResult describes a single known peer.
//
// (v1: internal/server/admin_api.go PeerInfoResult)
type PeerInfoResult struct {
	NodeID       string
	Address      string
	Status       string
	LastSeenUnix int64
	LatencyMs    int64
}

// KeyInfoResult reports per-replica sibling information for a single key.
//
// (v1: internal/server/admin_api.go KeyInfoResult)
type KeyInfoResult struct {
	Key            []byte
	Replicas       []ReplicaKeyInfoResult
	PreferenceList []string
}

// ReplicaKeyInfoResult is one replica's view of a key.
//
// (v1: internal/server/admin_api.go ReplicaKeyInfoResult)
type ReplicaKeyInfoResult struct {
	NodeID   string
	HasKey   bool
	Siblings []SiblingInfoResult
	Error    string
}

// SiblingInfoResult summarizes a single sibling without its value.
//
// (v1: internal/server/admin_api.go SiblingInfoResult)
type SiblingInfoResult struct {
	Timestamp int64
	ValueSize uint32
	Tombstone bool
}

// Health reports node health across storage and cluster subsystems.
//
// v1's Health() returned only *HealthResult (no error, since it was
// designed to always succeed locally). This stub adds an error return so
// it can report ErrNotImplemented per CONVENTIONS.md's scaffold rules; a
// real implementation can still make it effectively infallible.
//
// TODO(v2): probe a.storage.Stats() for storage health/latency and
// a.membership.ActivePeerCount()/TotalPeerCount() for cluster health,
// deriving overall Status (v1: internal/server/admin_api.go
// AdminAPI.Health).
func (a *AdminAPI) Health() (*HealthResult, error) {
	return nil, contracts.ErrNotImplemented
}

// ClusterInfo reports cluster membership and overall status.
//
// v1's ClusterInfo() returned only *ClusterInfoResult (no error); see
// Health's doc comment for why this stub adds one.
//
// TODO(v2): build ClusterInfoResult from a.membership.GetPeers(), deriving
// overall Status from the failed-peer ratio (v1:
// internal/server/admin_api.go AdminAPI.ClusterInfo).
func (a *AdminAPI) ClusterInfo() (*ClusterInfoResult, error) {
	return nil, contracts.ErrNotImplemented
}

// GetMetrics returns metrics in Prometheus text exposition format.
//
// TODO(v2): gather from an infra/observability metrics registry once one
// exists (v1: internal/server/admin_api.go AdminAPI.GetMetrics read
// prometheus.DefaultGatherer directly).
func (a *AdminAPI) GetMetrics() (string, error) {
	return "", contracts.ErrNotImplemented
}

// KeyInfo returns detailed per-replica information about key.
//
// TODO(v2): call a.storage.GetRaw(key) for the local replica's view; a
// fuller implementation should also query remote replicas via
// transport.Transport (v1: internal/server/admin_api.go AdminAPI.KeyInfo
// only inspected the local replica too).
func (a *AdminAPI) KeyInfo(key []byte) (*KeyInfoResult, error) {
	return nil, contracts.ErrNotImplemented
}

// TriggerCompaction manually triggers storage compaction for debugging.
//
// v1's TriggerCompaction() returned (bool, string) with no error (Pebble
// compacts automatically, so it always "succeeded" trivially); see
// Health's doc comment for why this stub adds one.
//
// TODO(v2): expose a manual compaction trigger on infra/storage/pebble.Store
// once Pebble is wired in (v1: internal/server/admin_api.go
// AdminAPI.TriggerCompaction).
func (a *AdminAPI) TriggerCompaction() (bool, string, error) {
	return false, "", contracts.ErrNotImplemented
}
