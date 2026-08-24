package failuredetector

import (
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

// ProbeHandler receives heartbeat probe results from FailureDetector.
type ProbeHandler interface {
	OnHeartbeatResult(nodeID node.NodeID, err error)
}

// FailureDetector is a protocol worker that probes peer liveness via Transport.
// It reports results directly to a ProbeHandler without managing timers or state machines.
//
// Probe Flow:
//
//	Probe(peerIDs) ──> transport.Heartbeat(peerID)
//	                         │
//	                         ▼ (On Response / Error)
//	                   handler.OnHeartbeatResult(peerID, err)
type FailureDetector struct {
	transport adapter.ClientTransport
	handler   ProbeHandler
}

// NewFailureDetector constructs a FailureDetector attached to transport and handler.
func NewFailureDetector(tr adapter.ClientTransport, handler ProbeHandler) *FailureDetector {
	return &FailureDetector{
		transport: tr,
		handler:   handler,
	}
}

// SetHandler sets or updates the probe handler.
func (fd *FailureDetector) SetHandler(h ProbeHandler) {
	fd.handler = h
}

// Probe pings all given peers over Transport.
func (fd *FailureDetector) Probe(peerIDs []node.NodeID) {
	for _, id := range peerIDs {
		if err := fd.transport.Heartbeat(id, 0); err != nil && fd.handler != nil {
			fd.handler.OnHeartbeatResult(id, err)
		}
	}
}

// ProbeOne pings a single peer over Transport.
func (fd *FailureDetector) ProbeOne(targetID node.NodeID) {
	if err := fd.transport.Heartbeat(targetID, 0); err != nil && fd.handler != nil {
		fd.handler.OnHeartbeatResult(targetID, err)
	}
}
