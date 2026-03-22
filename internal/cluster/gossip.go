package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
)

// NodeEntry represents this node's view of another peer in the gossip state.
type NodeEntry struct {
	NodeID    string     `json:"node_id"`
	Addr      string     `json:"addr"` // internal HTTP address
	Status    NodeStatus `json:"status"`
	Version   uint64     `json:"version"`    // logical version for LWW
	UpdatedAt int64      `json:"updated_at"` // Unix seconds
}

// GossipConfig holds gossip protocol tuning parameters.
type GossipConfig struct {
	Enabled    bool
	FanOut     int           // number of peers to gossip to per round (default 3)
	Interval   time.Duration // how often to gossip (default 1s)
	HTTPClient *http.Client  // optional; nil = default
}

// GossipConfigFromConfig converts a config.GossipConfig to a cluster.GossipConfig.
func GossipConfigFromConfig(cfg config.GossipConfig) GossipConfig {
	return GossipConfig{
		Enabled:  cfg.Enabled,
		FanOut:   cfg.FanOut,
		Interval: cfg.Interval,
	}
}

// Gossip manages peer state via gossip protocol.
type Gossip struct {
	nodeID     common.NodeID
	selfAddr   string // this node's internal HTTP address
	state      map[common.NodeID]*NodeEntry
	mu         sync.RWMutex
	membership *MembershipManager
	config     GossipConfig
	httpClient *http.Client
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// NewGossip creates a new Gossip instance.
func NewGossip(nodeID common.NodeID, selfAddr string, membership *MembershipManager, cfg GossipConfig) *Gossip {
	// Apply defaults
	if cfg.FanOut <= 0 {
		cfg.FanOut = 3
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 1 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Second}
	}

	g := &Gossip{
		nodeID:     nodeID,
		selfAddr:   selfAddr,
		state:      make(map[common.NodeID]*NodeEntry),
		membership: membership,
		config:     cfg,
		httpClient: httpClient,
		stopCh:     make(chan struct{}),
	}

	// Seed self entry
	g.state[nodeID] = &NodeEntry{
		NodeID:    string(nodeID),
		Addr:      selfAddr,
		Status:    NodeStatusActive,
		Version:   1,
		UpdatedAt: time.Now().Unix(),
	}

	return g
}

// Start begins the gossip spread loop if enabled.
func (g *Gossip) Start() {
	if !g.config.Enabled {
		return
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.spreadLoop()
	}()
}

// Stop halts the gossip protocol and waits for the goroutine to exit.
func (g *Gossip) Stop() {
	close(g.stopCh)
	g.wg.Wait()
}

// spreadLoop periodically selects FanOut random peers and exchanges gossip state.
func (g *Gossip) spreadLoop() {
	ticker := time.NewTicker(g.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			g.gossipRound()
		}
	}
}

// gossipRound picks up to FanOut random peers and calls the exchange endpoint on each.
func (g *Gossip) gossipRound() {
	peers := g.membership.GetPeers()
	if len(peers) == 0 {
		return
	}

	// Shuffle and pick min(FanOut, len(peers))
	indices := rand.Perm(len(peers))
	fanOut := g.config.FanOut
	if fanOut > len(peers) {
		fanOut = len(peers)
	}

	localState := g.GetState()

	for i := 0; i < fanOut; i++ {
		peer := peers[indices[i]]
		httpAddr := g.membership.GetHTTPAddress(peer.ID)
		if httpAddr == "" {
			continue
		}
		go g.exchange(httpAddr, localState)
	}
}

// exchange sends local gossip state to a peer and merges the response.
func (g *Gossip) exchange(peerHTTPAddr string, localState map[common.NodeID]*NodeEntry) {
	body, err := json.Marshal(localState)
	if err != nil {
		fmt.Printf("gossip: marshal error: %v\n", err)
		return
	}

	url := "http://" + peerHTTPAddr + "/internal/gossip/exchange"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Printf("gossip: create request error for %s: %v\n", peerHTTPAddr, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		fmt.Printf("gossip: exchange error with %s: %v\n", peerHTTPAddr, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("gossip: exchange non-200 from %s: %d\n", peerHTTPAddr, resp.StatusCode)
		return
	}

	var incoming map[common.NodeID]*NodeEntry
	if err := json.NewDecoder(resp.Body).Decode(&incoming); err != nil {
		fmt.Printf("gossip: decode response error from %s: %v\n", peerHTTPAddr, err)
		return
	}

	g.Merge(incoming)
}

// Merge applies LWW (last-write-wins) merge of incoming gossip state.
// For each entry where incoming.Version > existing.Version, the local state is
// updated and membership.UpdatePeerStatus is called.
func (g *Gossip) Merge(incoming map[common.NodeID]*NodeEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for id, entry := range incoming {
		// Never overwrite self entry from remote gossip
		if id == g.nodeID {
			continue
		}

		existing, ok := g.state[id]
		if !ok || entry.UpdatedAt > existing.UpdatedAt {
			// Deep copy to avoid aliasing
			copied := *entry
			g.state[id] = &copied

			// Propagate status change to membership manager
			if g.membership != nil {
				g.membership.UpdatePeerStatus(id, entry.Status)
			}
		}
	}
}

// GetState returns a copy of the current gossip state for exchange.
func (g *Gossip) GetState() map[common.NodeID]*NodeEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	copy := make(map[common.NodeID]*NodeEntry, len(g.state))
	for id, entry := range g.state {
		e := *entry
		copy[id] = &e
	}
	return copy
}

// MarkPeer writes the failure-detector's assessed status for a peer into the
// gossip state with the current wall-clock timestamp.  This ensures the
// assessed status (e.g. FAILED) propagates to other nodes on the next gossip
// round instead of being invisible until the peer calls SetSelf again.
func (g *Gossip) MarkPeer(nodeID common.NodeID, status NodeStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.state[nodeID]
	if !ok {
		entry = &NodeEntry{NodeID: string(nodeID)}
		g.state[nodeID] = entry
	}
	entry.Status = status
	entry.UpdatedAt = time.Now().Unix()
}

// SetSelf updates this node's own entry and increments the version.
func (g *Gossip) SetSelf(status NodeStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.state[g.nodeID]
	if !ok {
		entry = &NodeEntry{
			NodeID: string(g.nodeID),
			Addr:   g.selfAddr,
		}
		g.state[g.nodeID] = entry
	}

	entry.Status = status
	entry.Version++
	entry.UpdatedAt = time.Now().Unix()
}
