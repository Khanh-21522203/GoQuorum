package cluster

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
	"context"
	"math/rand"
)

// ReadRepairer handles read repair operations (Section 3)
type ReadRepairer struct {
	nodeID  common.NodeID
	rpc     RPCClient
	config  config.ReadRepairConfig
	metrics *ReadRepairMetrics
}

func NewReadRepairer(
	nodeID common.NodeID,
	rpc RPCClient,
	config config.ReadRepairConfig) *ReadRepairer {

	return &ReadRepairer{
		nodeID:  nodeID,
		rpc:     rpc,
		config:  config,
		metrics: NewReadRepairMetrics(),
	}
}

// TriggerRepair triggers read repair for stale replicas (Section 3.3)
func (rr *ReadRepairer) TriggerRepair(
	ctx context.Context,
	key []byte,
	merged []storage.Sibling,
	responses []ReadResponse) {

	if !rr.config.Enabled {
		return
	}

	// Probabilistic triggering (Section 3.4)
	if rand.Float64() > rr.config.Probability {
		return
	}

	rr.metrics.ReadRepairTriggered.Inc()

	// Check each replica for staleness (Section 3.3)
	for _, resp := range responses {
		if resp.Error != nil {
			continue
		}

		needsRepair := rr.checkNeedsRepair(merged, resp.SiblingSet)
		if needsRepair {
			// Async repair (Section 3.4)
			if rr.config.Async {
				go rr.sendRepair(context.Background(), resp.NodeID, key, merged)
			} else {
				rr.sendRepair(ctx, resp.NodeID, key, merged)
			}
		}
	}
}

// checkNeedsRepair checks if replica is missing any maximal versions (Section 3.3)
func (rr *ReadRepairer) checkNeedsRepair(
	merged []storage.Sibling,
	replicaSiblings *storage.SiblingSet) bool {

	if replicaSiblings == nil {
		return true // Replica has nothing, needs everything
	}

	// Check if replica has all maximal versions
	for _, mergedSib := range merged {
		if !rr.isDominatedOrEqual(mergedSib.VClock, replicaSiblings.Siblings) {
			return true // Replica is missing this version
		}
	}

	return false
}

// isDominatedOrEqual checks if vclock is dominated by or equal to any sibling (Section 3.3)
func (rr *ReadRepairer) isDominatedOrEqual(vClock vclock.VectorClock,
	siblings []storage.Sibling) bool {

	for _, sib := range siblings {
		comparison := vClock.Compare(sib.VClock)
		if comparison == vclock.Before || comparison == vclock.Equal {
			return true
		}
	}
	return false
}

// sendRepair sends repair to single replica (Section 3.3)
func (rr *ReadRepairer) sendRepair(
	ctx context.Context,
	nodeID common.NodeID,
	key []byte,
	merged []storage.Sibling) {

	repairCtx, cancel := context.WithTimeout(ctx, rr.config.Timeout)
	defer cancel()

	siblingSet := &storage.SiblingSet{Siblings: merged}

	// Skip self
	if nodeID == rr.nodeID {
		return
	}

	// Send repair
	err := rr.rpc.RemotePut(repairCtx, nodeID, key, siblingSet)

	if err != nil {
		rr.metrics.ReadRepairFailed.Inc()
		// Log but don't retry - anti-entropy will fix eventually
	} else {
		rr.metrics.ReadRepairSent.Inc()
		rr.metrics.ReadRepairKeysRepaired.Inc()
	}
}
