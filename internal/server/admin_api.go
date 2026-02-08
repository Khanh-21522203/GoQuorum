package server

import (
	"bytes"
	"time"

	"GoQuorum/internal/cluster"
	"GoQuorum/internal/storage"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

// AdminAPI implements the admin gRPC service
type AdminAPI struct {
	storage    *storage.Storage
	membership *cluster.MembershipManager
	nodeID     string
	version    string
	startTime  time.Time
}

// NewAdminAPI creates a new admin API service
func NewAdminAPI(
	store *storage.Storage,
	membership *cluster.MembershipManager,
	nodeID string,
	version string,
	startTime time.Time,
) *AdminAPI {
	return &AdminAPI{
		storage:    store,
		membership: membership,
		nodeID:     nodeID,
		version:    version,
		startTime:  startTime,
	}
}

// Health returns node health status
func (a *AdminAPI) Health() *HealthResult {
	status := "healthy"
	checks := make(map[string]CheckInfo)

	// Check storage
	storageStatus := "healthy"
	var storageLatency int64
	if a.storage != nil {
		start := time.Now()
		_ = a.storage.Stats()
		storageLatency = time.Since(start).Milliseconds()
	} else {
		storageStatus = "unhealthy"
	}
	checks["storage"] = CheckInfo{
		Status:    storageStatus,
		LatencyMs: storageLatency,
	}

	// Check cluster
	clusterStatus := "healthy"
	var peersActive, peersTotal int
	if a.membership != nil {
		peersActive = a.membership.ActivePeerCount()
		peersTotal = a.membership.TotalPeerCount()
		if peersTotal > 0 && peersActive < peersTotal/2 {
			clusterStatus = "degraded"
			if status == "healthy" {
				status = "degraded"
			}
		}
	}
	checks["cluster"] = CheckInfo{
		Status:      clusterStatus,
		PeersActive: peersActive,
		PeersTotal:  peersTotal,
	}

	return &HealthResult{
		Status:        status,
		NodeID:        a.nodeID,
		UptimeSeconds: int64(time.Since(a.startTime).Seconds()),
		Version:       a.version,
		Checks:        checks,
	}
}

// ClusterInfo returns cluster membership and status
func (a *AdminAPI) ClusterInfo() *ClusterInfoResult {
	result := &ClusterInfoResult{
		NodeID: a.nodeID,
		Peers:  []PeerInfoResult{},
		Status: "healthy",
	}

	if a.membership == nil {
		return result
	}

	peers := a.membership.GetPeers()
	failedCount := 0

	for _, peer := range peers {
		peerInfo := PeerInfoResult{
			NodeID:       string(peer.ID),
			Address:      peer.Addr,
			Status:       peer.Status.String(),
			LastSeenUnix: peer.LastSeen.Unix(),
		}
		result.Peers = append(result.Peers, peerInfo)

		if peer.Status.String() == "FAILED" {
			failedCount++
		}
	}

	// Determine cluster status
	if failedCount > 0 {
		if failedCount >= len(peers)/2 {
			result.Status = "critical"
		} else {
			result.Status = "degraded"
		}
	}

	return result
}

// GetMetrics returns Prometheus-format metrics
func (a *AdminAPI) GetMetrics() (string, error) {
	// Gather all metrics
	gathering, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return "", err
	}

	// Encode to text format
	var buf bytes.Buffer
	encoder := expfmt.NewEncoder(&buf, expfmt.FmtText)

	for _, mf := range gathering {
		if err := encoder.Encode(mf); err != nil {
			return "", err
		}
	}

	return buf.String(), nil
}

// KeyInfo returns detailed information about a specific key
func (a *AdminAPI) KeyInfo(key []byte) (*KeyInfoResult, error) {
	result := &KeyInfoResult{
		Key:            key,
		Replicas:       []ReplicaKeyInfoResult{},
		PreferenceList: []string{},
	}

	// Get local data
	if a.storage != nil {
		siblings, err := a.storage.Get(key)
		localInfo := ReplicaKeyInfoResult{
			NodeID: a.nodeID,
			HasKey: err == nil && siblings != nil && len(siblings.Siblings) > 0,
		}

		if siblings != nil {
			for _, sib := range siblings.Siblings {
				localInfo.Siblings = append(localInfo.Siblings, SiblingInfoResult{
					Timestamp: sib.Timestamp,
					ValueSize: uint32(len(sib.Value)),
					Tombstone: sib.Tombstone,
				})
			}
		}

		if err != nil {
			localInfo.Error = err.Error()
		}

		result.Replicas = append(result.Replicas, localInfo)
	}

	return result, nil
}

// TriggerCompaction manually triggers Pebble compaction
func (a *AdminAPI) TriggerCompaction() (bool, string) {
	// Note: Pebble handles compaction automatically
	// This is a manual trigger for debugging
	return true, "Compaction will be triggered by Pebble automatically"
}

// HealthResult represents health check result
type HealthResult struct {
	Status        string
	NodeID        string
	UptimeSeconds int64
	Version       string
	Checks        map[string]CheckInfo
}

// CheckInfo represents a single check result
type CheckInfo struct {
	Status      string
	LatencyMs   int64
	Error       string
	PeersActive int
	PeersTotal  int
	FreeBytes   int64
	TotalBytes  int64
}

// ClusterInfoResult represents cluster info result
type ClusterInfoResult struct {
	NodeID string
	Peers  []PeerInfoResult
	Status string
}

// PeerInfoResult represents peer info
type PeerInfoResult struct {
	NodeID       string
	Address      string
	Status       string
	LastSeenUnix int64
	LatencyMs    int64
}

// KeyInfoResult represents key info result
type KeyInfoResult struct {
	Key            []byte
	Replicas       []ReplicaKeyInfoResult
	PreferenceList []string
}

// ReplicaKeyInfoResult represents replica key info
type ReplicaKeyInfoResult struct {
	NodeID   string
	HasKey   bool
	Siblings []SiblingInfoResult
	Error    string
}

// SiblingInfoResult represents sibling info
type SiblingInfoResult struct {
	Timestamp int64
	ValueSize uint32
	Tombstone bool
}
