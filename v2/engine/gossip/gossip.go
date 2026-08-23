package gossip

import (
	"math/rand"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter/transport"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/statemachine"
)

// NodeEntry is one node's gossiped membership state.
type NodeEntry struct {
	NodeID    node.NodeID
	Addr      string // Internal address used for the gossip exchange.
	Status    membership.NodeStatus
	Version   uint64 // Logical version for last-writer-wins merge.
	UpdatedAt int64  // Unix timestamp (seconds).
}

// GossipConfig controls gossip fan-out and cadence.
type GossipConfig struct {
	Enabled  bool
	FanOut   int           // Peers contacted per round.
	Interval time.Duration // Gossip round period.
}

// gossipState is the lifecycle state of a Gossip instance's round loop.
type gossipState int

const (
	stateIdle gossipState = iota
	stateRunning
	stateStopped
)

// gossipTrigger is the set of events that drive the lifecycle machine.
type gossipTrigger int

const (
	triggerStart gossipTrigger = iota
	triggerRoundTimer
	triggerStop
)

// Gossip periodically exchanges membership state with a random subset of
// peers.
//
// Gossip is driven entirely by a reactor.Reactor: Start and Stop only ever
// arm or disarm a repeating timer, and every method that touches g.state is
// only ever called from the reactor's single goroutine (directly, or via a
// timer callback). That single-threading is what lets g.state be a plain
// map with no mutex.
type Gossip struct {
	nodeID     node.NodeID
	selfAddr   string
	state      map[node.NodeID]*NodeEntry
	membership *membership.MembershipManager
	transport  transport.Transport
	reactor    *reactor.Reactor
	config     GossipConfig

	lifecycle *statemachine.Machine[gossipState, gossipTrigger]
	timerID   reactor.TimerID
}

// NewGossip creates a gossip runner for the local node.
//
// FanOut and Interval fall back to 3 peers and 1 second respectively when
// left unset, so a caller can pass a zero-value GossipConfig and still get a
// working cadence.
func NewGossip(nodeID node.NodeID, selfAddr string, mm *membership.MembershipManager, tr transport.Transport, rt *reactor.Reactor, cfg GossipConfig) *Gossip {
	if cfg.FanOut <= 0 {
		cfg.FanOut = 3
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}

	g := &Gossip{
		nodeID:     nodeID,
		selfAddr:   selfAddr,
		state:      make(map[node.NodeID]*NodeEntry),
		membership: mm,
		transport:  tr,
		reactor:    rt,
		config:     cfg,
	}

	// Seed the local node's own entry so the very first round has something
	// to gossip about the local node, even before SetSelf is ever called.
	g.state[nodeID] = &NodeEntry{
		NodeID:    nodeID,
		Addr:      selfAddr,
		Status:    membership.NodeStatusActive,
		Version:   1,
		UpdatedAt: time.Now().Unix(),
	}

	g.lifecycle = statemachine.New(stateIdle, []statemachine.Edge[gossipState, gossipTrigger]{
		{From: stateIdle, To: stateRunning, Trigger: triggerStart, Action: g.onStart},
		{From: stateRunning, To: stateRunning, Trigger: triggerRoundTimer, Action: g.onRoundTimer},
		{From: stateRunning, To: stateStopped, Trigger: triggerStop, Action: g.onStop},
	})

	return g
}

// Start begins the periodic gossip round loop. It is a no-op if
// config.Enabled is false, or if the loop is already running or stopped.
func (g *Gossip) Start() {
	if !g.config.Enabled {
		return
	}
	_ = g.lifecycle.Handle(triggerStart)
}

// Stop halts the gossip round loop. It is a no-op if the loop was never
// started.
func (g *Gossip) Stop() {
	_ = g.lifecycle.Handle(triggerStop)
}

// onStart arms the repeating round timer. The timer callback only ever
// requests the triggerRoundTimer transition rather than calling runRound
// directly, so a timer that fires after Stop (e.g. one already queued when
// CancelTimer runs) finds no edge from stateStopped and is silently
// dropped instead of running a round.
func (g *Gossip) onStart() error {
	g.timerID = g.reactor.ScheduleEvery(g.config.Interval, func() {
		_ = g.lifecycle.Handle(triggerRoundTimer)
	})
	return nil
}

// onRoundTimer runs one gossip round as the action of the stateRunning ->
// stateRunning self-transition.
func (g *Gossip) onRoundTimer() error {
	g.runRound()
	return nil
}

// onStop disarms the round timer.
func (g *Gossip) onStop() error {
	g.reactor.CancelTimer(g.timerID)
	return nil
}

// runRound gossips the local state to a random subset of peers and merges
// each reply as it arrives. A peer that fails or has no known address is
// simply skipped: gossip is a best-effort, self-healing protocol, so a
// single round need not retry or succeed for every peer.
func (g *Gossip) runRound() {
	peers := g.membership.GetAllPeers()
	if len(peers) == 0 {
		return
	}

	fanOut := g.config.FanOut
	if fanOut > len(peers) {
		fanOut = len(peers)
	}

	entries := stateToEntries(g.GetState())
	for _, i := range rand.Perm(len(peers))[:fanOut] {
		peerID := peers[i]
		if g.membership.GetHTTPAddress(peerID) == "" {
			continue
		}
		g.transport.GossipExchange(peerID, entries, func(reply []transport.GossipEntry, err error) {
			if err != nil {
				return
			}
			g.Merge(entriesToState(reply))
		})
	}
}

// Merge merges incoming gossiped state into the local view using
// last-writer-wins by UpdatedAt. The local node's own entry is never
// overwritten by an incoming one: only SetSelf and MarkPeer may change it,
// so a stale copy of it echoed back by a peer can never regress it.
func (g *Gossip) Merge(incoming map[node.NodeID]*NodeEntry) {
	for id, entry := range incoming {
		if id == g.nodeID {
			continue
		}

		existing, ok := g.state[id]
		if ok && entry.UpdatedAt <= existing.UpdatedAt {
			continue
		}

		copied := *entry
		g.state[id] = &copied

		if g.membership != nil {
			g.membership.UpdatePeerStatus(id, entry.Status)
		}
	}
}

// GetState returns a defensive copy of the current gossip state: callers
// (a gossip round preparing an outgoing exchange, or a test) may hold onto
// the returned map, and must not observe later mutations of g.state through
// it.
func (g *Gossip) GetState() map[node.NodeID]*NodeEntry {
	out := make(map[node.NodeID]*NodeEntry, len(g.state))
	for id, entry := range g.state {
		copied := *entry
		out[id] = &copied
	}
	return out
}

// MarkPeer stamps a peer's assessed status for propagation on the next
// gossip round, creating its entry if this is the first time the peer has
// been observed.
func (g *Gossip) MarkPeer(nodeID node.NodeID, status membership.NodeStatus) {
	entry, ok := g.state[nodeID]
	if !ok {
		entry = &NodeEntry{NodeID: nodeID}
		g.state[nodeID] = entry
	}
	entry.Status = status
	entry.UpdatedAt = time.Now().Unix()
}

// SetSelf updates the local node's own gossiped status, incrementing its
// version so peers can tell the update apart from a stale replay of the
// previous status at the same, or an earlier, timestamp.
func (g *Gossip) SetSelf(status membership.NodeStatus) {
	entry, ok := g.state[g.nodeID]
	if !ok {
		entry = &NodeEntry{NodeID: g.nodeID, Addr: g.selfAddr}
		g.state[g.nodeID] = entry
	}
	entry.Status = status
	entry.Version++
	entry.UpdatedAt = time.Now().Unix()
}

// stateToEntries flattens local gossip state to the wire shape the
// transport port exchanges.
func stateToEntries(state map[node.NodeID]*NodeEntry) []transport.GossipEntry {
	entries := make([]transport.GossipEntry, 0, len(state))
	for _, entry := range state {
		entries = append(entries, transport.GossipEntry{
			NodeID:    entry.NodeID,
			Addr:      entry.Addr,
			Status:    uint8(entry.Status),
			Version:   entry.Version,
			UpdatedAt: entry.UpdatedAt,
		})
	}
	return entries
}

// entriesToState rebuilds a gossip state map from the transport port's wire
// shape.
func entriesToState(entries []transport.GossipEntry) map[node.NodeID]*NodeEntry {
	state := make(map[node.NodeID]*NodeEntry, len(entries))
	for _, entry := range entries {
		state[entry.NodeID] = &NodeEntry{
			NodeID:    entry.NodeID,
			Addr:      entry.Addr,
			Status:    membership.NodeStatus(entry.Status),
			Version:   entry.Version,
			UpdatedAt: entry.UpdatedAt,
		}
	}
	return state
}
