package harness

import (
	"fmt"

	"goquorum.io/v2/client"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/infra/config"
	"goquorum.io/v2/server/app"
)

// Node is a single in-process GoQuorum v2 node under test. It wraps
// server/app.Server, the real composition root, plumbed through from a
// caller-supplied config.Config.
type Node struct {
	ID   node.NodeID
	Addr string // Client-facing HTTP address (cfg.Server.HTTPAddr).

	srv *app.Server
}

// StartNode builds and starts a single in-process node from cfg via
// server/app.New followed by (*app.Server).Start.
//
// TODO(v2): this currently always returns a non-nil error, since
// infra/storage/pebble.NewStore (called transitively by app.New) is
// itself a stub returning contracts.ErrNotImplemented (see
// infra/storage/pebble/store.go). Once storage and transport are
// implemented, this will actually boot a listening node.
func StartNode(cfg *config.Config) (*Node, error) {
	srv, err := app.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("harness: start node %s: %w", cfg.Node.NodeID, err)
	}

	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("harness: start node %s: %w", cfg.Node.NodeID, err)
	}

	return &Node{ID: cfg.Node.NodeID, Addr: cfg.Server.HTTPAddr, srv: srv}, nil
}

// Stop gracefully stops the node's server. It is safe to call on a nil
// Node or a Node whose server never started.
func (n *Node) Stop() {
	if n == nil || n.srv == nil {
		return
	}
	n.srv.Stop()
}

// Client returns a client.Client dialed at this node's HTTP address, using
// client.DefaultClientConfig for the dial/request/retry defaults.
func (n *Node) Client() (*client.Client, error) {
	return client.NewClient(client.DefaultClientConfig(n.Addr))
}
