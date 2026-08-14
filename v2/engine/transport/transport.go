package transport

import (
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/storage"
)

// GossipEntry is one node's gossiped membership state, mirrored here (the
// same workaround engine/readrepair.ReplicaRead uses) so this port can
// expose a gossip exchange without engine/transport importing
// engine/gossip, which would create a cycle back through engine/gossip's
// own dependency on this package.
type GossipEntry struct {
	NodeID    node.NodeID
	Addr      string
	Status    uint8
	Version   uint64
	UpdatedAt int64 // Unix timestamp (seconds).
}

// Transport is the [PORT] implemented by infra/transport. It carries all
// node-to-node communication engine needs: replicated writes/reads,
// heartbeats, anti-entropy root exchange, membership gossip, and
// graceful-leave notification.
//
// Every method is callback-based rather than blocking: engine subsystems
// run on a single-threaded engine/reactor.Reactor, so no engine call may
// block waiting on a remote peer. done is invoked exactly once, from the
// same reactor goroutine that issued the call, once the operation
// completes or fails. There is no context.Context parameter: cancellation
// and timeouts are expressed as a reactor-scheduled timer racing the
// callback instead of a deadline threaded through every call.
//
// v1 (internal/cluster/rpc_client.go RPCClient) also declared SendHeartbeat
// as a separate method; it was a pure alias of Heartbeat with an identical
// signature, so v2 keeps only Heartbeat and drops the duplicate. v1's
// gossip also talked to peers directly rather than through RPCClient; v2
// promotes that exchange onto this port as GossipExchange.
type Transport interface {
	// RemotePut replicates a write to node id.
	RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error))
	// RemoteGet reads a key's sibling set from node id.
	RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error))
	// Heartbeat pings node id for liveness.
	Heartbeat(id node.NodeID, done func(error))
	// GetMerkleRoot fetches node id's current anti-entropy Merkle root.
	GetMerkleRoot(id node.NodeID, done func([]byte, error))
	// NotifyLeaving informs node id that the local node is leaving the
	// cluster gracefully.
	NotifyLeaving(id node.NodeID, done func(error))
	// GossipExchange sends the local node's gossip state to node id and
	// returns its reply, for membership dissemination.
	GossipExchange(id node.NodeID, entries []GossipEntry, done func([]GossipEntry, error))
	// Close releases all resources held by the transport.
	Close() error
}
