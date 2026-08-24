package gossip

import (
	"math/rand"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

// GossipHandler receives remote gossip entries exchanged during a round.
type GossipHandler interface {
	OnGossipReceived(peerID node.NodeID, entries []adapter.GossipEntry)
}

// GossipConfig controls gossip fan-out.
type GossipConfig struct {
	FanOut int // Peers contacted per round.
}

// Gossip is a protocol worker that exchanges membership state with peers via Transport.
// It reports replies back to a GossipHandler without managing its own timers or state machines.
//
// Exchange Flow:
//
//	Round(peers, localEntries)
//	       │
//	       ├── 1. Select random subset of peers (size = FanOut)
//	       └── 2. For each peer:
//	                └── transport.GossipExchange(peer, localEntries)
//	                      └── On reply ──> handler.OnGossipReceived(peer, reply)
type Gossip struct {
	transport adapter.ClientTransport
	handler   GossipHandler
	fanOut    int
}

// NewGossip constructs a Gossip protocol worker.
func NewGossip(tr adapter.ClientTransport, handler GossipHandler, cfg GossipConfig) *Gossip {
	fanOut := cfg.FanOut
	if fanOut <= 0 {
		fanOut = 3
	}
	return &Gossip{
		transport: tr,
		handler:   handler,
		fanOut:    fanOut,
	}
}

// SetHandler sets or updates the gossip handler.
func (g *Gossip) SetHandler(h GossipHandler) {
	g.handler = h
}

// Round executes a single gossip exchange round with a random subset of peers.
func (g *Gossip) Round(peers []node.NodeID, localEntries []adapter.GossipEntry) {
	if len(peers) == 0 {
		return
	}

	fanOut := g.fanOut
	if fanOut > len(peers) {
		fanOut = len(peers)
	}

	for _, i := range rand.Perm(len(peers))[:fanOut] {
		peerID := peers[i]
		_ = g.transport.GossipExchange(peerID, 0, localEntries)
	}
}
