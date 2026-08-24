package readrepair

import (
	"math/rand"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
)

// ReplicaRead is one replica's response to the read that triggered a repair check.
type ReplicaRead struct {
	NodeID     node.NodeID
	SiblingSet *adapter.SiblingSet
	Error      error
}

// ReadRepairer probabilistically repairs stale replicas in the background after quorum reads.
type ReadRepairer struct {
	nodeID    node.NodeID
	transport adapter.ClientTransport
	config    config.ReadRepairConfig
}

// NewReadRepairer creates a read repairer for the local node.
func NewReadRepairer(nodeID node.NodeID, tr adapter.ClientTransport, cfg config.ReadRepairConfig) *ReadRepairer {
	return &ReadRepairer{nodeID: nodeID, transport: tr, config: cfg}
}

// TriggerRepair probabilistically detects and fixes stale replicas using causal dominance.
//
// Repair Flow:
//
//	Merged Siblings ──> Check each replica's responses:
//	                      │
//	                      ├── Stale (missing newer vclock)? ──> transport.RemotePut(replica, key, merged)
//	                      └── Up-to-date / Dominating       ──> Skip (no-op)
func (rr *ReadRepairer) TriggerRepair(key []byte, merged []adapter.Sibling, responses []ReplicaRead) {
	if !rr.config.Enabled {
		return
	}
	if rr.config.Probability < 1 && rand.Float64() >= rr.config.Probability {
		return
	}

	repaired := &adapter.SiblingSet{Siblings: merged}
	for _, resp := range responses {
		if resp.Error != nil {
			continue
		}
		if !isStale(resp.SiblingSet, merged) {
			continue
		}
		_ = rr.transport.RemotePut(resp.NodeID, 0, key, repaired)
	}
}

// isStale reports whether current is missing any sibling from merged (by vector clock dominance).
func isStale(current *adapter.SiblingSet, merged []adapter.Sibling) bool {
	var have []adapter.Sibling
	if current != nil {
		have = current.Siblings
	}
	for _, m := range merged {
		covered := false
		for _, h := range have {
			if h.VClock.Dominates(m.VClock) {
				covered = true
				break
			}
		}
		if !covered {
			return true
		}
	}
	return false
}
