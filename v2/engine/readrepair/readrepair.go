package readrepair

import (
	"math/rand"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
)

// ReplicaRead is one replica's response to the read that triggered a
// repair check.
type ReplicaRead struct {
	NodeID     node.NodeID
	SiblingSet *adapter.SiblingSet
	Error      error
}

// ReadRepairer probabilistically checks quorum-read responses for stale
// replicas and repairs them by re-writing the merged sibling set.
type ReadRepairer struct {
	nodeID    node.NodeID
	transport adapter.Transport
	config    config.ReadRepairConfig
}

// NewReadRepairer creates a read repairer for the local node.
func NewReadRepairer(nodeID node.NodeID, tr adapter.Transport, cfg config.ReadRepairConfig) *ReadRepairer {
	return &ReadRepairer{nodeID: nodeID, transport: tr, config: cfg}
}

// TriggerRepair probabilistically checks each replica in responses against
// the already-merged sibling set for key, and repairs (via RemotePut) any
// replica whose siblings are dominated by merged.
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
		rr.transport.RemotePut(resp.NodeID, key, repaired, func(error) {})
	}
}

// isStale reports whether current is missing information present in
// merged: it is stale if merged holds any sibling that none of current's
// own siblings already dominates.
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
