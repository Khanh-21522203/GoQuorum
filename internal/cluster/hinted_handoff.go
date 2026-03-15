package cluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/storage"
)

const (
	maxHintAge         = 24 * time.Hour
	maxHintsPerNode    = 1000
	hintReplayInterval = 30 * time.Second
)

// Hint records a write that failed to reach its target node.
type Hint struct {
	Key       []byte
	Siblings  *storage.SiblingSet
	CreatedAt time.Time
}

// HintedHandoff stores failed writes as hints and replays them once the
// target node comes back online.
type HintedHandoff struct {
	mu    sync.Mutex
	hints map[common.NodeID][]*Hint // target node → pending hints

	membership *MembershipManager
	rpc        RPCClient
	nodeID     common.NodeID

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewHintedHandoff creates a new HintedHandoff instance.
func NewHintedHandoff(membership *MembershipManager, rpc RPCClient, nodeID common.NodeID) *HintedHandoff {
	return &HintedHandoff{
		hints:      make(map[common.NodeID][]*Hint),
		membership: membership,
		rpc:        rpc,
		nodeID:     nodeID,
		stopCh:     make(chan struct{}),
	}
}

// Start begins the background hint replay loop.
func (hh *HintedHandoff) Start() {
	hh.wg.Add(1)
	go hh.replayLoop()
}

// Stop shuts down the background replay loop and waits for it to exit.
func (hh *HintedHandoff) Stop() {
	close(hh.stopCh)
	hh.wg.Wait()
}

// StoreHint records a write that could not be delivered to targetNodeID.
// If the per-node hint count would exceed maxHintsPerNode, the oldest hint
// is dropped to make room for the new one.
func (hh *HintedHandoff) StoreHint(targetNodeID common.NodeID, key []byte, siblings *storage.SiblingSet) error {
	if targetNodeID == hh.nodeID {
		return fmt.Errorf("cannot store hint for self")
	}

	hint := &Hint{
		Key:       append([]byte(nil), key...),
		Siblings:  siblings,
		CreatedAt: time.Now(),
	}

	hh.mu.Lock()
	defer hh.mu.Unlock()

	list := hh.hints[targetNodeID]

	// Evict oldest entry if at capacity.
	if len(list) >= maxHintsPerNode {
		list = list[1:]
	}

	hh.hints[targetNodeID] = append(list, hint)
	return nil
}

// HintCount returns the number of pending hints for a given node.
func (hh *HintedHandoff) HintCount(nodeID common.NodeID) int {
	hh.mu.Lock()
	defer hh.mu.Unlock()
	return len(hh.hints[nodeID])
}

// replayLoop periodically checks whether hinted nodes have recovered and
// attempts to deliver their pending hints.
func (hh *HintedHandoff) replayLoop() {
	defer hh.wg.Done()

	ticker := time.NewTicker(hintReplayInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hh.stopCh:
			return
		case <-ticker.C:
			hh.replayAll()
		}
	}
}

// replayAll iterates over all nodes that have pending hints and attempts
// replay for nodes that are now active.
func (hh *HintedHandoff) replayAll() {
	hh.mu.Lock()
	// Collect the set of target nodes that currently have hints.
	targets := make([]common.NodeID, 0, len(hh.hints))
	for nodeID := range hh.hints {
		targets = append(targets, nodeID)
	}
	hh.mu.Unlock()

	for _, nodeID := range targets {
		if hh.membership.GetPeerStatus(nodeID) == NodeStatusActive {
			hh.replayHintsForNode(nodeID)
		}
	}
}

// replayHintsForNode attempts to deliver all pending hints for nodeID.
// Successfully delivered hints are removed; failed hints are retained.
// Returns the number of hints successfully delivered.
func (hh *HintedHandoff) replayHintsForNode(nodeID common.NodeID) int {
	hh.mu.Lock()
	list := hh.hints[nodeID]
	if len(list) == 0 {
		hh.mu.Unlock()
		return 0
	}
	// Work on a snapshot; we will replace the list after replay.
	snapshot := make([]*Hint, len(list))
	copy(snapshot, list)
	hh.mu.Unlock()

	now := time.Now()
	delivered := 0
	remaining := make([]*Hint, 0, len(snapshot))

	for _, hint := range snapshot {
		// Drop hints that are too old regardless of delivery outcome.
		if now.Sub(hint.CreatedAt) > maxHintAge {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := hh.rpc.RemotePut(ctx, nodeID, hint.Key, hint.Siblings)
		cancel()

		if err == nil {
			delivered++
		} else {
			remaining = append(remaining, hint)
		}
	}

	hh.mu.Lock()
	if len(remaining) == 0 {
		delete(hh.hints, nodeID)
	} else {
		hh.hints[nodeID] = remaining
	}
	hh.mu.Unlock()

	return delivered
}
