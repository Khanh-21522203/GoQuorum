package client

import (
	"context"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/vclock"
)

// Sibling represents one version of a value returned by a Get. It mirrors
// engine/storage.Sibling but is exposed with client-facing field naming
// (Context instead of VClock) and without the storage-only ExpiresAt field.
//
// (v1: client/client.go Sibling)
type Sibling struct {
	Value     []byte
	Context   vclock.VectorClock
	Timestamp int64
	Tombstone bool
}

// Client is a high-level GoQuorum client that talks to a single server
// address.
//
// v1 wrapped a gRPC *grpc.ClientConn and a generated stub here. v2 keeps
// only the resolved configuration until the real transport lands.
//
// TODO(v2): import google.golang.org/grpc; add a conn *grpc.ClientConn and
// a generated client stub field, populated by NewClient (v1:
// client/client.go Client).
type Client struct {
	config ClientConfig
}

// NewClient stores cfg and returns a ready-to-use Client. It does not yet
// dial the server.
//
// TODO(v2): dial cfg.Addr over gRPC within cfg.DialTimeout, using insecure
// transport credentials for now and grpc.WithBlock(), and populate the
// connection/stub fields on Client (v1: client/client.go NewClient).
func NewClient(cfg ClientConfig) (*Client, error) {
	return &Client{config: cfg}, nil
}

// Get retrieves all sibling values for key. The returned siblings may
// include concurrent versions; use a ConflictResolver to merge them.
//
// TODO(v2): call the Get RPC within cfg.RequestTimeout, retrying transient
// failures up to cfg.MaxRetries with exponential backoff seeded by
// cfg.RetryBaseDelay, and convert the wire response into []Sibling,
// mapping a not-found status to a sentinel error (v1: client/client.go
// Client.Get).
func (c *Client) Get(ctx context.Context, key []byte) ([]Sibling, error) {
	return nil, contracts.ErrNotImplemented
}

// Put stores value for key. causal should be the context returned by a
// prior Get (pass vclock.NewVectorClock() for a blind write). It returns
// the new causal context assigned by the server.
//
// TODO(v2): call the Put RPC within cfg.RequestTimeout, retrying transient
// failures up to cfg.MaxRetries with exponential backoff seeded by
// cfg.RetryBaseDelay, sending causal and returning the server's resulting
// vector clock (v1: client/client.go Client.Put).
func (c *Client) Put(ctx context.Context, key, value []byte, causal vclock.VectorClock) (vclock.VectorClock, error) {
	return vclock.VectorClock{}, contracts.ErrNotImplemented
}

// Delete removes key by writing a tombstone. causal must come from a prior
// Get.
//
// TODO(v2): call the Delete RPC within cfg.RequestTimeout, retrying
// transient failures up to cfg.MaxRetries with exponential backoff seeded
// by cfg.RetryBaseDelay (v1: client/client.go Client.Delete).
func (c *Client) Delete(ctx context.Context, key []byte, causal vclock.VectorClock) error {
	return contracts.ErrNotImplemented
}

// Close closes the underlying connection to the server.
//
// TODO(v2): close the grpc.ClientConn (v1: client/client.go Client.Close).
func (c *Client) Close() error {
	return nil
}
