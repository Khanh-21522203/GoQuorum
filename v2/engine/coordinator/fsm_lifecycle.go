package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

// coordinatorState represents the coordinator subsystem lifecycle.
//
// Lifecycle:
//
//	[coordinatorNotStarted] ──(coordinatorTriggerStart)──> [coordinatorRunning] ──(coordinatorTriggerStop)──> [coordinatorStopped]
type coordinatorState int

const (
	coordinatorNotStarted coordinatorState = iota
	coordinatorRunning
	coordinatorStopped
)

// coordinatorTrigger drives the Coordinator lifecycle machine.
type coordinatorTrigger int

const (
	coordinatorTriggerStart coordinatorTrigger = iota
	coordinatorTriggerStop
)

// Start starts background anti-entropy sync and arms master reactor timers.
func (c *Coordinator) Start() error {
	return c.handleLifecycle(coordinatorTriggerStart)
}

// Stop stops background timers and subsystems.
func (c *Coordinator) Stop() {
	_ = c.handleLifecycle(coordinatorTriggerStop)
}

func (c *Coordinator) handleLifecycle(trigger coordinatorTrigger) error {
	switch c.state {
	case coordinatorNotStarted:
		if trigger == coordinatorTriggerStart {
			return c.transitionLifecycle(coordinatorRunning)
		}
	case coordinatorRunning:
		if trigger == coordinatorTriggerStop {
			return c.transitionLifecycle(coordinatorStopped)
		}
	}
	return nil
}

func (c *Coordinator) transitionLifecycle(next coordinatorState) error {
	c.state = next
	return c.enterLifecycle(next)
}

func (c *Coordinator) enterLifecycle(s coordinatorState) error {
	switch s {
	case coordinatorRunning:
		if err := c.antiEntropy.Build(); err != nil {
			return err
		}
		c.armTimers()
	case coordinatorStopped:
		c.disarmTimers()
	}
	return nil
}

func (c *Coordinator) armTimers() {
	if c.failureDetector != nil && c.failureDetectorConfig.HeartbeatInterval > 0 {
		c.heartbeatTimer = c.reactor.ScheduleEvery(c.failureDetectorConfig.HeartbeatInterval, func() {
			c.failureDetector.Probe(c.getPeerIDs())
		})
	}
	if c.gossip != nil && c.gossipInterval > 0 {
		c.gossipTimer = c.reactor.ScheduleEvery(c.gossipInterval, func() {
			c.gossip.Round(c.getPeerIDs(), c.getLocalGossipEntries())
		})
	}
	if c.handoff != nil && c.handoffInterval > 0 {
		c.handoffTimer = c.reactor.ScheduleEvery(c.handoffInterval, func() {
			c.handoff.Replay(c.getActivePeerIDs())
		})
	}
	if c.antiEntropy != nil && c.antiEntropyConfig.Enabled && c.antiEntropyConfig.ScanInterval > 0 {
		c.antiEntropyTimer = c.reactor.ScheduleEvery(c.antiEntropyConfig.ScanInterval, func() {
			c.antiEntropy.ScanTick(c.getPeerIDs())
		})
	}
}

func (c *Coordinator) disarmTimers() {
	c.reactor.CancelTimer(c.heartbeatTimer)
	c.reactor.CancelTimer(c.gossipTimer)
	c.reactor.CancelTimer(c.handoffTimer)
	c.reactor.CancelTimer(c.antiEntropyTimer)
}

func (c *Coordinator) getPeerIDs() []node.NodeID {
	if c.membership != nil {
		return c.membership.GetAllPeers()
	}
	return nil
}

func (c *Coordinator) getActivePeerIDs() []node.NodeID {
	if c.membership != nil {
		return c.membership.GetActivePeers()
	}
	return nil
}

func (c *Coordinator) getLocalGossipEntries() []adapter.GossipEntry {
	if c.membership == nil {
		return nil
	}
	peers := c.membership.GetPeers()
	entries := make([]adapter.GossipEntry, 0, len(peers)+1)
	entries = append(entries, adapter.GossipEntry{
		NodeID:    c.nodeID,
		Addr:      c.membership.GetAddress(c.nodeID),
		Status:    uint8(c.membership.GetLocalStatus()),
		Version:   1,
		UpdatedAt: time.Now().Unix(),
	})
	for _, p := range peers {
		entries = append(entries, adapter.GossipEntry{
			NodeID:    p.ID,
			Addr:      p.Addr,
			Status:    uint8(p.Status),
			Version:   1,
			UpdatedAt: time.Now().Unix(),
		})
	}
	return entries
}
