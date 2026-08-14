package readrepair

import (
	"math/rand"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

// ReplicaRead is one replica's response to the read that triggered a
// repair check.
//
// v1's ReadRepairer took v1's coordinator.ReadResponse (defined in
// internal/cluster/coordinator.go) as a parameter type. v2's
// engine/coordinator does not expose an equivalent type in its spec
// surface, and importing it here would create an import cycle
// (coordinator composes readrepair). This package therefore declares its
// own equivalent, with the same fields.
type ReplicaRead struct {
	NodeID     node.NodeID
	SiblingSet *storage.SiblingSet
	Error      error
}

// ReadRepairer probabilistically checks quorum-read responses for stale
// replicas and repairs them by re-writing the merged sibling set.
//
// v1's ReadRepairer also held a *ReadRepairMetrics field backed by
// prometheus.Counter/Histogram. engine imports only the standard library
// and contracts, so that field is dropped here; a real implementation
// should report metrics through an infra-level adapter instead.
//
// (v1: internal/cluster/read_repair.go ReadRepairer)
type ReadRepairer struct {
	nodeID    node.NodeID
	transport transport.Transport
	config    config.ReadRepairConfig
}

// NewReadRepairer creates a read repairer for the local node.
//
// TODO(v2): store dependencies (v1: internal/cluster/read_repair.go
// NewReadRepairer).
func NewReadRepairer(nodeID node.NodeID, tr transport.Transport, cfg config.ReadRepairConfig) *ReadRepairer {
	return &ReadRepairer{nodeID: nodeID, transport: tr, config: cfg}
}

// TriggerRepair probabilistically checks each replica in responses against
// the already-merged sibling set for key, and repairs (via RemotePut) any
// replica whose siblings are dominated by merged.
//
// The trigger probability is drawn once per call, not once per stale
// replica: a repair pass either happens in full or is skipped in full, so
// the odds a given read causes any repair traffic at all match
// config.Probability exactly.
func (rr *ReadRepairer) TriggerRepair(key []byte, merged []storage.Sibling, responses []ReplicaRead) {
	if !rr.config.Enabled {
		return
	}
	if rr.config.Probability < 1 && rand.Float64() >= rr.config.Probability {
		return
	}

	repaired := &storage.SiblingSet{Siblings: merged}
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
// own siblings already dominates. A replica whose siblings already
// dominate every element of merged has nothing to gain from a repair.
func isStale(current *storage.SiblingSet, merged []storage.Sibling) bool {
	var have []storage.Sibling
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
