package handoff

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/statemachine"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

// maxHintAge bounds how long a hint may sit in the buffer before it is
// dropped without ever being replayed.
const maxHintAge = 24 * time.Hour

// maxHintsPerNode bounds how many hints may be buffered for a single
// target node before the oldest one is evicted to make room.
const maxHintsPerNode = 1000

// hintReplayInterval is how often the reactor attempts to replay buffered
// hints to nodes that have become active again.
const hintReplayInterval = 30 * time.Second

// Hint is a single buffered write awaiting replay to its intended target
// node.
type Hint struct {
	Key       []byte
	Siblings  *storage.SiblingSet
	CreatedAt time.Time
}

// lifecycleState is the run state of a HintedHandoff instance.
type lifecycleState int

const (
	lifecycleIdle lifecycleState = iota
	lifecycleRunning
	lifecycleStopped
)

// lifecycleTrigger drives lifecycleState transitions.
type lifecycleTrigger int

const (
	triggerStart lifecycleTrigger = iota
	triggerReplayTick
	triggerStop
)

// newLifecycle builds the Idle -> Running -> Stopped machine that guards
// HintedHandoff's Start/replay/Stop sequencing. The Running -> Running
// self-loop on triggerReplayTick lets replay confirm it is still meant to
// run before touching the hint buffer, without needing its own boolean
// flag: a stray tick delivered after Stop (e.g. one already queued on the
// reactor when CancelTimer runs) is simply rejected as an invalid
// transition instead of being replayed.
func newLifecycle() *statemachine.Machine[lifecycleState, lifecycleTrigger] {
	return statemachine.New(lifecycleIdle, []statemachine.Edge[lifecycleState, lifecycleTrigger]{
		{From: lifecycleIdle, To: lifecycleRunning, Trigger: triggerStart},
		{From: lifecycleRunning, To: lifecycleRunning, Trigger: triggerReplayTick},
		{From: lifecycleRunning, To: lifecycleStopped, Trigger: triggerStop},
	})
}

// HintedHandoff buffers writes for nodes that are temporarily unreachable
// and replays them once the target node is observed active again.
//
// Every exported method must be called from the reactor goroutine passed
// to NewHintedHandoff: the hint buffer carries no lock, relying instead on
// the single-threaded guarantee the reactor provides.
type HintedHandoff struct {
	hints      map[node.NodeID][]*Hint
	membership *membership.MembershipManager
	transport  transport.Transport
	nodeID     node.NodeID
	reactor    *reactor.Reactor
	lifecycle  *statemachine.Machine[lifecycleState, lifecycleTrigger]
	timerID    reactor.TimerID
}

// NewHintedHandoff creates a hinted-handoff buffer for the local node,
// driven by r.
func NewHintedHandoff(mm *membership.MembershipManager, tr transport.Transport, nodeID node.NodeID, r *reactor.Reactor) *HintedHandoff {
	return &HintedHandoff{
		hints:      make(map[node.NodeID][]*Hint),
		membership: mm,
		transport:  tr,
		nodeID:     nodeID,
		reactor:    r,
		lifecycle:  newLifecycle(),
	}
}

// Start launches the periodic replay loop. Calling Start more than once is
// a no-op.
func (hh *HintedHandoff) Start() {
	if err := hh.lifecycle.Handle(triggerStart); err != nil {
		return
	}
	hh.timerID = hh.reactor.ScheduleEvery(hintReplayInterval, hh.replay)
}

// Stop halts the replay loop. Calling Stop before Start, or more than
// once, is a no-op.
func (hh *HintedHandoff) Stop() {
	if err := hh.lifecycle.Handle(triggerStop); err != nil {
		return
	}
	hh.reactor.CancelTimer(hh.timerID)
}

// StoreHint buffers a write for targetNodeID, to be replayed once that
// node is reachable again. It evicts the oldest hint for that node if
// already at capacity.
func (hh *HintedHandoff) StoreHint(targetNodeID node.NodeID, key []byte, siblings *storage.SiblingSet) error {
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

// replay is the periodic timer callback: it attempts one delivery of each
// buffered hint whose target node currently appears active.
func (hh *HintedHandoff) replay() {
	if err := hh.lifecycle.Handle(triggerReplayTick); err != nil {
		return
	}
	if len(hh.hints) == 0 {
		return
	}

	active := make(map[node.NodeID]struct{})
	for _, id := range hh.membership.GetActivePeers() {
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

		// Take the whole buffer for this node up front. A hint that fails
		// to replay is pushed back onto hh.hints[nodeID] from within its
		// own done callback below, so it is retried on the next tick
		// rather than immediately within this one.
		hh.hints[nodeID] = nil

		for _, hint := range pending {
			// A hint this old is superseded by anti-entropy already, so
			// replaying it would only waste a round trip; drop it.
			if now.Sub(hint.CreatedAt) > maxHintAge {
				continue
			}
			hh.transport.RemotePut(nodeID, hint.Key, hint.Siblings, func(err error) {
				if err != nil {
					hh.hints[nodeID] = append(hh.hints[nodeID], hint)
				}
			})
		}
	}
}
