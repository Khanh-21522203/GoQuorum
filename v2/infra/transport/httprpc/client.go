package httprpc

import (
	"time"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

// ClientConfig configures the HTTP/JSON inter-node client's connection
// pool, dial/reconnect behaviour, and TLS.
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

// Client implements engine/transport.Transport over HTTP/JSON.
type Client struct {
	cfg     ClientConfig
	localID node.NodeID
}

var _ adapter.ClientTransport = (*Client)(nil)

// NewClient creates an HTTP/JSON inter-node client for the local node
// localID.
func NewClient(cfg ClientConfig, localID node.NodeID) *Client {
	return &Client{cfg: cfg, localID: localID}
}

// RemotePut replicates a write to node id.
func (c *Client) RemotePut(id node.NodeID, corrID uint64, key []byte, siblings *adapter.SiblingSet) error {
	return contracts.ErrNotImplemented
}

// RemoteGet reads a key's sibling set from node id.
func (c *Client) RemoteGet(id node.NodeID, corrID uint64, key []byte) error {
	return contracts.ErrNotImplemented
}

// Heartbeat pings node id for liveness.
func (c *Client) Heartbeat(id node.NodeID, corrID uint64) error {
	return contracts.ErrNotImplemented
}

// GetMerkleRoot fetches node id's current anti-entropy Merkle root.
func (c *Client) GetMerkleRoot(id node.NodeID, corrID uint64) error {
	return contracts.ErrNotImplemented
}

// NotifyLeaving informs node id that the local node is leaving the cluster
// gracefully.
func (c *Client) NotifyLeaving(id node.NodeID, corrID uint64) error {
	return contracts.ErrNotImplemented
}

// GossipExchange sends the local node's gossip state to node id and
// returns its reply, for membership dissemination.
func (c *Client) GossipExchange(id node.NodeID, corrID uint64, entries []adapter.GossipEntry) error {
	return contracts.ErrNotImplemented
}

// Dial initiates an asynchronous connection to addr for node id.
func (c *Client) Dial(id node.NodeID, addr string) error {
	return contracts.ErrNotImplemented
}

// Close releases all resources held by the transport.
func (c *Client) Close() error {
	return contracts.ErrNotImplemented
}
