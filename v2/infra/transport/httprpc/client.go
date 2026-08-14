package httprpc

import (
	"time"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

// ClientConfig configures the HTTP/JSON inter-node client's connection
// pool, dial/reconnect behaviour, and TLS.
//
// (v1: internal/config/connection.go ConnectionConfig)
type ClientConfig struct {
	PoolSize    int
	IdleTimeout time.Duration
	MaxLifetime time.Duration
	DialTimeout time.Duration

	ReconnectBase        time.Duration
	ReconnectMax         time.Duration
	ReconnectFactor      float64
	MaxReconnectAttempts int

	TLSEnabled bool
}

// Client implements engine/transport.Transport over HTTP/JSON. Peer
// addresses are resolved by whatever address book the caller wires in (v1
// resolved them via *cluster.MembershipManager.GetHTTPAddress).
//
// (v1: internal/cluster/rpc_client.go GRPCClient)
type Client struct {
	cfg     ClientConfig
	localID node.NodeID
}

var _ transport.Transport = (*Client)(nil)

// NewClient creates an HTTP/JSON inter-node client for the local node
// localID.
//
// TODO(v2): import net/http; build an *http.Transport from cfg
// (MaxIdleConns, MaxIdleConnsPerHost, IdleConnTimeout) and, if
// cfg.TLSEnabled, load a client TLS config via infra/security (v1:
// internal/cluster/rpc_client.go NewGRPCClient).
func NewClient(cfg ClientConfig, localID node.NodeID) *Client {
	return &Client{cfg: cfg, localID: localID}
}

// RemotePut replicates a write to node id.
//
// TODO(v2): resolve id's HTTP address, then POST each sibling to
// <addr>/internal/replicate, failing on the first unsuccessful response
// (v1: internal/cluster/rpc_client.go GRPCClient.RemotePut).
func (c *Client) RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// RemoteGet reads a key's sibling set from node id.
//
// TODO(v2): POST to <addr>/internal/read and decode the sibling set from
// the JSON response; a "not found" response is not itself an error (v1:
// internal/cluster/rpc_client.go GRPCClient.RemoteGet).
func (c *Client) RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// Heartbeat pings node id for liveness.
//
// TODO(v2): POST to <addr>/internal/heartbeat (v1:
// internal/cluster/rpc_client.go GRPCClient.SendHeartbeat/Heartbeat; v2
// drops the duplicate alias, see engine/transport's doc comment).
func (c *Client) Heartbeat(id node.NodeID, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// GetMerkleRoot fetches node id's current anti-entropy Merkle root.
//
// TODO(v2): POST to <addr>/internal/merkle-root and return the decoded
// root bytes (v1: internal/cluster/rpc_client.go
// GRPCClient.GetMerkleRoot).
func (c *Client) GetMerkleRoot(id node.NodeID, done func([]byte, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// NotifyLeaving informs node id that the local node is leaving the cluster
// gracefully.
//
// TODO(v2): POST to <addr>/internal/notify-leaving (v1:
// internal/cluster/rpc_client.go GRPCClient.NotifyLeaving).
func (c *Client) NotifyLeaving(id node.NodeID, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// GossipExchange sends the local node's gossip state to node id and
// returns its reply, for membership dissemination.
//
// TODO(v2): POST to <addr>/internal/gossip with entries as the JSON body
// and decode the peer's reply entries from the response.
func (c *Client) GossipExchange(id node.NodeID, entries []transport.GossipEntry, done func([]transport.GossipEntry, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// Close releases all resources held by the transport.
//
// TODO(v2): close idle connections on the underlying http.Client (v1:
// internal/cluster/rpc_client.go GRPCClient.Close).
func (c *Client) Close() error {
	return contracts.ErrNotImplemented
}
