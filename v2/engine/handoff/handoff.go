package handoff

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

const (
	maxHintAge      = 24 * time.Hour
	maxHintsPerNode = 1000
)

// Hint is a single buffered write awaiting replay to its target node.
type Hint struct {
	Key       []byte
	Siblings  *adapter.SiblingSet
	CreatedAt time.Time
}

// HintedHandoff buffers writes for unreachable nodes and replays them when nodes recover.
//
// Replay Flow:
//
//	Replay(activePeers)
//	       │
//	       └── For each active peer with buffered hints:
//	             └── transport.RemotePut(peer, key, siblings)
//	                   ├── Success ──> Remove hint
//	                   └── Failure ──> Requeue hint for next replay
type HintedHandoff struct {
	hints     map[node.NodeID][]*Hint
	transport adapter.ClientTransport
	nodeID    node.NodeID
}

// NewHintedHandoff creates a hinted-handoff buffer for the local node.
func NewHintedHandoff(tr adapter.ClientTransport, nodeID node.NodeID) *HintedHandoff {
	return &HintedHandoff{
		hints:     make(map[node.NodeID][]*Hint),
		transport: tr,
		nodeID:    nodeID,
	}
}

// StoreHint buffers a write for targetNodeID, evicting the oldest if at capacity.
func (hh *HintedHandoff) StoreHint(targetNodeID node.NodeID, key []byte, siblings *adapter.SiblingSet) error {
	hint := &Hint{
		Key:       append([]byte(nil), key...),
		Siblings:  siblings,
		CreatedAt: time.Now(),
	}

	list := hh.hints[targetNodeID]
	if len(list) >= maxHintsPerNode {
		list = list[1:]
	}
	hh.hints[targetNodeID] = append(list, hint)
	return nil
}

// HintCount returns the number of hints currently buffered for nodeID.
func (hh *HintedHandoff) HintCount(nodeID node.NodeID) int {
	return len(hh.hints[nodeID])
}

// Replay attempts to deliver buffered hints to all currently active peers.
func (hh *HintedHandoff) Replay(activePeers []node.NodeID) {
	if len(hh.hints) == 0 || len(activePeers) == 0 {
		return
	}

	active := make(map[node.NodeID]struct{}, len(activePeers))
	for _, id := range activePeers {
		active[id] = struct{}{}
	}

	now := time.Now()
	for nodeID, pending := range hh.hints {
		if len(pending) == 0 {
			continue
		}
		if _, ok := active[nodeID]; !ok {
			continue
		}

		targetID := nodeID
		hh.hints[targetID] = nil

		for _, hint := range pending {
			h := hint
			if now.Sub(h.CreatedAt) > maxHintAge {
				continue
			}
			if err := hh.transport.RemotePut(targetID, 0, h.Key, h.Siblings); err != nil {
				hh.hints[targetID] = append(hh.hints[targetID], h)
			}
		}
	}
}
