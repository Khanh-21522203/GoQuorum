package wire

import "goquorum.io/v2/contracts/node"

// GossipEntry is one node's gossiped membership state transferred over the wire.
type GossipEntry struct {
	NodeID    node.NodeID
	Addr      string
	Status    uint8
	Version   uint64
	UpdatedAt int64 // Unix timestamp (seconds).
}
