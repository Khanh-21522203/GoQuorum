package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/antientropy"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/failuredetector"
	"goquorum.io/v2/engine/gossip"
	"goquorum.io/v2/engine/handoff"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/readrepair"
	"goquorum.io/v2/engine/statemachine"
)

// PutOptions carries per-request write tuning for a Put.
type PutOptions struct {
	TTLSeconds int64 // 0 = no TTL; >0 = key expires this many seconds from now.
}

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

// peerTrigger drives a single peer's NodeState transitions in Coordinator.
type peerTrigger int

const (
	triggerHeartbeatOK peerTrigger = iota
	triggerHeartbeatMissed
	triggerThresholdReached
)

// peerEntry bundles a tracked peer's heartbeat bookkeeping with its state machine.
type peerEntry struct {
	id      node.NodeID
	health  *node.NodeHealth
	machine *statemachine.Machine[node.NodeState, peerTrigger]
	misses  int
}

// requestState represents the resolution lifecycle of an in-flight quorum request.
//
// Quorum Resolution State Machine:
//
//	                   ┌─── triggerQuorumReached ────> [requestSucceeded]
//	                   │                               (>= W or R acks)
//	[requestAwaiting] ─┼─── triggerQuorumUnreachable ──> [requestFailed]
//	                   │                               (too many failures)
//	                   └─── triggerTimeout ───────────> [requestFailed]
//	                                                   (client deadline)
type requestState int

const (
	requestAwaiting  requestState = iota // Waiting on replica responses.
	requestSucceeded                     // Quorum reached; caller callback completed.
	requestFailed                        // Quorum unreachable or timed out.
)

// requestTrigger is the set of events driving requestState transitions.
type requestTrigger int

const (
	triggerReplicaSuccess    requestTrigger = iota // Replica call succeeded.
	triggerReplicaFailure                          // Replica call failed.
	triggerQuorumReached                           // Success count reached required quorum.
	triggerQuorumUnreachable                       // Remaining replicas cannot achieve quorum.
	triggerTimeout                                 // Client request deadline elapsed.
)

// writeRequest tracks in-flight Put or Delete replica fan-out across N replicas.
type writeRequest struct {
	id           uint64
	total        int // Number of replicas contacted (N).
	quorum       int // Required success count (W).
	successCount int
	failureCount int
	resolve      func(error) // Invoked once on quorum resolution.
	timerID      reactor.TimerID
	machine      *statemachine.Machine[requestState, requestTrigger]
}

// readRequest tracks in-flight Get replica fan-out across N replicas.
type readRequest struct {
	id           uint64
	key          []byte
	total        int // Number of replicas contacted (N).
	quorum       int // Required success count (R).
	successCount int
	failureCount int
	responses    []readrepair.ReplicaRead // Collected replica responses in arrival order.
	resolve      func([]adapter.Sibling, error)
	timerID      reactor.TimerID
	machine      *statemachine.Machine[requestState, requestTrigger]
}

// Coordinator is the central brain orchestrating quorum reads/writes, cluster membership,
// peer failure detection, gossip dissemination, hinted handoff, and anti-entropy.
// All public methods dispatch onto the reactor goroutine via PostFunc.
type Coordinator struct {
	nodeID     node.NodeID
	ring       *hashring.HashRing
	storage    adapter.Storage
	transport  adapter.Transport
	membership *membership.MembershipManager
	reactor    *reactor.Reactor

	quorumConfig          config.QuorumConfig
	readRepairConfig      config.ReadRepairConfig
	timeoutConfig         config.TimeoutConfig
	antiEntropyConfig     config.AntiEntropyConfig
	failureDetectorConfig config.FailureDetectorConfig
	gossipConfig          gossip.GossipConfig
	gossipInterval        time.Duration
	handoffInterval       time.Duration

	readRepairer    *readrepair.ReadRepairer
	antiEntropy     *antientropy.AntiEntropy
	failureDetector *failuredetector.FailureDetector
	gossip          *gossip.Gossip
	handoff         *handoff.HintedHandoff

	lifecycle *statemachine.Machine[coordinatorState, coordinatorTrigger]

	peers map[node.NodeID]*peerEntry

	heartbeatTimer   reactor.TimerID
	gossipTimer      reactor.TimerID
	handoffTimer     reactor.TimerID
	antiEntropyTimer reactor.TimerID

	requestSeq    uint64
	writeRequests map[uint64]*writeRequest
	readRequests  map[uint64]*readRequest
}

// Option configures optional Coordinator tuning.
type Option func(*Coordinator)

// WithReadRepairConfig configures read-repair behavior.
func WithReadRepairConfig(cfg config.ReadRepairConfig) Option {
	return func(c *Coordinator) { c.readRepairConfig = cfg }
}

// WithAntiEntropyConfig configures anti-entropy behavior.
func WithAntiEntropyConfig(cfg config.AntiEntropyConfig) Option {
	return func(c *Coordinator) { c.antiEntropyConfig = cfg }
}

// WithFailureDetectorConfig configures failure detector behavior.
func WithFailureDetectorConfig(cfg config.FailureDetectorConfig) Option {
	return func(c *Coordinator) { c.failureDetectorConfig = cfg }
}

// WithGossipConfig configures gossip behavior.
func WithGossipConfig(cfg gossip.GossipConfig, interval time.Duration) Option {
	return func(c *Coordinator) { c.gossipConfig = cfg; c.gossipInterval = interval }
}

// WithTimeoutConfig configures timeout behavior.
func WithTimeoutConfig(cfg config.TimeoutConfig) Option {
	return func(c *Coordinator) { c.timeoutConfig = cfg }
}

// NewCoordinator constructs a coordinator attached to storage, transport, ring, and reactor.
func NewCoordinator(
	id node.NodeID,
	ring *hashring.HashRing,
	store adapter.Storage,
	tr adapter.Transport,
	mm *membership.MembershipManager,
	rt *reactor.Reactor,
	cfg config.QuorumConfig,
	opts ...Option,
) *Coordinator {
	c := &Coordinator{
		nodeID:                id,
		ring:                  ring,
		storage:               store,
		transport:             tr,
		membership:            mm,
		reactor:               rt,
		quorumConfig:          cfg,
		readRepairConfig:      config.DefaultReadRepairConfig(),
		antiEntropyConfig:     config.DefaultAntiEntropyConfig(),
		failureDetectorConfig: config.DefaultFailureDetectorConfig(),
		timeoutConfig:         config.DefaultTimeoutConfig(),
		gossipConfig:          gossip.GossipConfig{FanOut: 3},
		gossipInterval:        time.Second,
		handoffInterval:       30 * time.Second,
		writeRequests:         make(map[uint64]*writeRequest),
		readRequests:          make(map[uint64]*readRequest),
		peers:                 make(map[node.NodeID]*peerEntry),
	}

	for _, opt := range opts {
		opt(c)
	}

	c.readRepairer = readrepair.NewReadRepairer(id, tr, c.readRepairConfig)
	c.antiEntropy = antientropy.NewAntiEntropy(id, store, ring, tr, c.antiEntropyConfig)
	c.failureDetector = failuredetector.NewFailureDetector(tr, c)
	c.gossip = gossip.NewGossip(tr, c, c.gossipConfig)
	c.handoff = handoff.NewHintedHandoff(tr, id)

	if mm != nil {
		for _, p := range mm.GetAllPeers() {
			c.peers[p] = c.newPeerEntry(p)
		}
	}

	c.lifecycle = statemachine.New(coordinatorNotStarted, []statemachine.Edge[coordinatorState, coordinatorTrigger]{
		{From: coordinatorNotStarted, To: coordinatorRunning, Trigger: coordinatorTriggerStart, Action: func() error {
			if err := c.antiEntropy.Build(); err != nil {
				return err
			}
			c.armTimers()
			return nil
		}},
		{From: coordinatorRunning, To: coordinatorStopped, Trigger: coordinatorTriggerStop, Action: func() error {
			c.disarmTimers()
			return nil
		}},
	})

	return c
}

// Start starts background anti-entropy sync and arms master reactor timers.
func (c *Coordinator) Start() error {
	return c.lifecycle.Handle(coordinatorTriggerStart)
}

// Stop stops background timers and subsystems.
func (c *Coordinator) Stop() {
	_ = c.lifecycle.Handle(coordinatorTriggerStop)
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

func (c *Coordinator) peerEdges(id node.NodeID) []statemachine.Edge[node.NodeState, peerTrigger] {
	recoverAction := func() error {
		if c.membership != nil {
			c.membership.UpdatePeerStatus(id, membership.NodeStatusActive)
		}
		_ = c.ring.UpdateNodeState(id, node.NodeStateActive)
		return nil
	}
	failAction := func() error {
		if c.membership != nil {
			c.membership.UpdatePeerStatus(id, membership.NodeStatusFailed)
		}
		_ = c.ring.UpdateNodeState(id, node.NodeStateFailed)
		return nil
	}
	return []statemachine.Edge[node.NodeState, peerTrigger]{
		{From: node.NodeStateUnknown, Trigger: triggerHeartbeatOK, To: node.NodeStateActive, Action: recoverAction},
		{From: node.NodeStateActive, Trigger: triggerHeartbeatOK, To: node.NodeStateActive},
		{From: node.NodeStateDegraded, Trigger: triggerHeartbeatOK, To: node.NodeStateActive, Action: recoverAction},
		{From: node.NodeStateFailed, Trigger: triggerHeartbeatOK, To: node.NodeStateActive, Action: recoverAction},
		{From: node.NodeStateLeaving, Trigger: triggerHeartbeatOK, To: node.NodeStateLeaving},

		{From: node.NodeStateUnknown, Trigger: triggerHeartbeatMissed, To: node.NodeStateUnknown},
		{From: node.NodeStateActive, Trigger: triggerHeartbeatMissed, To: node.NodeStateDegraded},
		{From: node.NodeStateDegraded, Trigger: triggerHeartbeatMissed, To: node.NodeStateDegraded},
		{From: node.NodeStateLeaving, Trigger: triggerHeartbeatMissed, To: node.NodeStateLeaving},

		{From: node.NodeStateUnknown, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateActive, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateDegraded, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateLeaving, Trigger: triggerThresholdReached, To: node.NodeStateFailed, Action: failAction},
		{From: node.NodeStateFailed, Trigger: triggerThresholdReached, To: node.NodeStateFailed},
	}
}

func (c *Coordinator) newPeerEntry(id node.NodeID) *peerEntry {
	return &peerEntry{
		id:      id,
		health:  &node.NodeHealth{NodeID: id, State: node.NodeStateActive},
		machine: statemachine.New(node.NodeStateActive, c.peerEdges(id)),
	}
}

// OnHeartbeatResult implements failuredetector.ProbeHandler.
func (c *Coordinator) OnHeartbeatResult(nodeID node.NodeID, err error) {
	entry, ok := c.peers[nodeID]
	if !ok {
		entry = c.newPeerEntry(nodeID)
		c.peers[nodeID] = entry
	}

	if err != nil {
		entry.misses++
		entry.health.MissedHeartbeats = entry.misses
		if entry.misses >= 3 {
			_ = entry.machine.Handle(triggerThresholdReached)
		} else {
			_ = entry.machine.Handle(triggerHeartbeatMissed)
		}
		return
	}

	entry.misses = 0
	entry.health.MissedHeartbeats = 0
	entry.health.LastHeartbeat = time.Now()
	_ = entry.machine.Handle(triggerHeartbeatOK)
}

// OnGossipReceived implements gossip.GossipHandler.
func (c *Coordinator) OnGossipReceived(peerID node.NodeID, entries []adapter.GossipEntry) {
	if c.membership == nil {
		return
	}
	for _, entry := range entries {
		if entry.NodeID == c.nodeID {
			continue
		}
		status := membership.NodeStatus(entry.Status)
		c.membership.UpdatePeerStatus(entry.NodeID, status)
		var nodeState node.NodeState
		switch status {
		case membership.NodeStatusActive:
			nodeState = node.NodeStateActive
		case membership.NodeStatusSuspect:
			nodeState = node.NodeStateDegraded
		case membership.NodeStatusFailed:
			nodeState = node.NodeStateFailed
		case membership.NodeStatusLeaving:
			nodeState = node.NodeStateLeaving
		default:
			nodeState = node.NodeStateUnknown
		}
		_ = c.ring.UpdateNodeState(entry.NodeID, nodeState)
	}
}

// Membership returns the encapsulated MembershipManager.
func (c *Coordinator) Membership() *membership.MembershipManager {
	return c.membership
}

// GetClusterView returns the current membership status of the cluster.
func (c *Coordinator) GetClusterView() map[node.NodeID]membership.NodeStatus {
	if c.membership == nil {
		return nil
	}
	return c.membership.GetClusterView()
}

// GetPeers returns peer info from membership.
func (c *Coordinator) GetPeers() []node.PeerInfo {
	if c.membership == nil {
		return nil
	}
	return c.membership.GetPeers()
}

// GetActivePeers returns active peer IDs.
func (c *Coordinator) GetActivePeers() []node.NodeID {
	if c.membership == nil {
		return nil
	}
	return c.membership.GetActivePeers()
}

func (c *Coordinator) nextRequestID() uint64 {
	c.requestSeq++
	return c.requestSeq
}

// Put performs a quorum write of value under key.
func (c *Coordinator) Put(key string, value []byte, causal vclock.VectorClock, done func(vclock.VectorClock, error), opts ...PutOptions) {
	c.reactor.PostFunc(func() {
		c.doPut(key, value, causal, done, opts...)
	})
}

func (c *Coordinator) doPut(key string, value []byte, causal vclock.VectorClock, done func(vclock.VectorClock, error), opts ...PutOptions) {
	tick := causal.Copy()
	tick.Tick(c.nodeID)

	var expiresAt int64
	if len(opts) > 0 && opts[0].TTLSeconds > 0 {
		expiresAt = time.Now().Unix() + opts[0].TTLSeconds
	}

	siblingSet := &adapter.SiblingSet{
		Siblings: []adapter.Sibling{{
			Value:     value,
			VClock:    tick,
			Timestamp: time.Now().Unix(),
			ExpiresAt: expiresAt,
		}},
	}

	prefList, err := c.ring.GetPreferenceList(key, c.quorumConfig.N)
	if err != nil {
		done(vclock.VectorClock{}, err)
		return
	}

	req := c.newWriteRequest(len(prefList), c.quorumConfig.W, "put", func(err error) {
		if err != nil {
			done(vclock.VectorClock{}, err)
			return
		}
		done(tick, nil)
	})

	keyBytes := []byte(key)
	for _, nodeID := range prefList {
		reqID := req.id
		targetNodeID := nodeID
		cb := func(err error) {
			if err != nil && targetNodeID != c.nodeID && c.handoff != nil {
				_ = c.handoff.StoreHint(targetNodeID, keyBytes, siblingSet)
			}
			c.onWriteReplicaResult(reqID, err)
		}
		if nodeID == c.nodeID {
			c.storage.Put(keyBytes, siblingSet, cb)
		} else {
			c.transport.RemotePut(nodeID, keyBytes, siblingSet, cb)
		}
	}
}

// Get performs a quorum read of key, merging concurrent siblings and triggering read-repair.
func (c *Coordinator) Get(key string, done func([]adapter.Sibling, error)) {
	c.reactor.PostFunc(func() {
		c.doGet(key, done)
	})
}

func (c *Coordinator) doGet(key string, done func([]adapter.Sibling, error)) {
	prefList, err := c.ring.GetPreferenceList(key, c.quorumConfig.N)
	if err != nil {
		done(nil, err)
		return
	}

	keyBytes := []byte(key)
	req := c.newReadRequest(keyBytes, len(prefList), c.quorumConfig.R, done)

	for _, nodeID := range prefList {
		reqID, nid := req.id, nodeID
		cb := func(ss *adapter.SiblingSet, err error) { c.onReadReplicaResult(reqID, nid, ss, err) }
		if nodeID == c.nodeID {
			c.storage.Get(keyBytes, cb)
		} else {
			c.transport.RemoteGet(nodeID, keyBytes, cb)
		}
	}
}

// Delete performs a quorum tombstone write for key.
func (c *Coordinator) Delete(key string, causal vclock.VectorClock, done func(error)) {
	c.reactor.PostFunc(func() {
		c.doDelete(key, causal, done)
	})
}

func (c *Coordinator) doDelete(key string, causal vclock.VectorClock, done func(error)) {
	tick := causal.Copy()
	tick.Tick(c.nodeID)

	siblingSet := &adapter.SiblingSet{
		Siblings: []adapter.Sibling{{
			Tombstone: true,
			VClock:    tick,
			Timestamp: time.Now().Unix(),
		}},
	}

	prefList, err := c.ring.GetPreferenceList(key, c.quorumConfig.N)
	if err != nil {
		done(err)
		return
	}

	req := c.newWriteRequest(len(prefList), c.quorumConfig.W, "delete", done)

	keyBytes := []byte(key)
	for _, nodeID := range prefList {
		reqID := req.id
		targetNodeID := nodeID
		cb := func(err error) {
			if err != nil && targetNodeID != c.nodeID && c.handoff != nil {
				_ = c.handoff.StoreHint(targetNodeID, keyBytes, siblingSet)
			}
			c.onWriteReplicaResult(reqID, err)
		}
		if nodeID == c.nodeID {
			c.storage.Put(keyBytes, siblingSet, cb)
		} else {
			c.transport.RemotePut(nodeID, keyBytes, siblingSet, cb)
		}
	}
}

// GetMerkleRoot returns the coordinator's current anti-entropy Merkle root.
func (c *Coordinator) GetMerkleRoot() []byte {
	return c.antiEntropy.GetMerkleRoot()
}

func (c *Coordinator) newWriteRequest(total, quorum int, op string, resolve func(error)) *writeRequest {
	req := &writeRequest{id: c.nextRequestID(), total: total, quorum: quorum, resolve: resolve}

	req.machine = statemachine.New(requestAwaiting, []statemachine.Edge[requestState, requestTrigger]{
		{From: requestAwaiting, To: requestAwaiting, Trigger: triggerReplicaSuccess, Action: func() error {
			req.successCount++
			return nil
		}},
		{From: requestAwaiting, To: requestAwaiting, Trigger: triggerReplicaFailure, Action: func() error {
			req.failureCount++
			return nil
		}},
		{From: requestSucceeded, To: requestSucceeded, Trigger: triggerReplicaSuccess, Action: func() error {
			req.successCount++
			return nil
		}},
		{From: requestSucceeded, To: requestSucceeded, Trigger: triggerReplicaFailure, Action: func() error {
			req.failureCount++
			return nil
		}},
		{From: requestFailed, To: requestFailed, Trigger: triggerReplicaSuccess, Action: func() error {
			req.successCount++
			return nil
		}},
		{From: requestFailed, To: requestFailed, Trigger: triggerReplicaFailure, Action: func() error {
			req.failureCount++
			return nil
		}},
		{From: requestAwaiting, To: requestSucceeded, Trigger: triggerQuorumReached, Action: func() error {
			c.reactor.CancelTimer(req.timerID)
			req.resolve(nil)
			return nil
		}},
		{From: requestAwaiting, To: requestFailed, Trigger: triggerQuorumUnreachable, Action: func() error {
			c.reactor.CancelTimer(req.timerID)
			req.resolve(newQuorumError(op, quorum, req.successCount))
			return nil
		}},
		{From: requestAwaiting, To: requestFailed, Trigger: triggerTimeout, Action: func() error {
			req.resolve(newQuorumError(op, quorum, req.successCount))
			return nil
		}},
	})

	c.writeRequests[req.id] = req
	req.timerID = c.reactor.ScheduleOnce(c.timeoutConfig.ClientTimeout, func() {
		c.onWriteTimeout(req.id)
	})
	return req
}

func (c *Coordinator) onWriteReplicaResult(reqID uint64, err error) {
	req, ok := c.writeRequests[reqID]
	if !ok {
		return
	}

	alreadyResolved := req.machine.State() != requestAwaiting
	if err == nil {
		_ = req.machine.Handle(triggerReplicaSuccess)
	} else {
		_ = req.machine.Handle(triggerReplicaFailure)
	}

	if !alreadyResolved {
		switch {
		case req.successCount >= req.quorum:
			_ = req.machine.Handle(triggerQuorumReached)
		case req.total-req.failureCount < req.quorum:
			_ = req.machine.Handle(triggerQuorumUnreachable)
		}
	}

	if req.successCount+req.failureCount >= req.total {
		delete(c.writeRequests, req.id)
	}
}

func (c *Coordinator) onWriteTimeout(reqID uint64) {
	req, ok := c.writeRequests[reqID]
	if !ok || req.machine.State() != requestAwaiting {
		return
	}
	delete(c.writeRequests, reqID)
	_ = req.machine.Handle(triggerTimeout)
}

func (c *Coordinator) newReadRequest(key []byte, total, quorum int, resolve func([]adapter.Sibling, error)) *readRequest {
	req := &readRequest{id: c.nextRequestID(), key: key, total: total, quorum: quorum, resolve: resolve}

	req.machine = statemachine.New(requestAwaiting, []statemachine.Edge[requestState, requestTrigger]{
		{From: requestAwaiting, To: requestAwaiting, Trigger: triggerReplicaSuccess, Action: func() error {
			req.successCount++
			return nil
		}},
		{From: requestAwaiting, To: requestAwaiting, Trigger: triggerReplicaFailure, Action: func() error {
			req.failureCount++
			return nil
		}},
		{From: requestSucceeded, To: requestSucceeded, Trigger: triggerReplicaSuccess, Action: func() error {
			req.successCount++
			return nil
		}},
		{From: requestSucceeded, To: requestSucceeded, Trigger: triggerReplicaFailure, Action: func() error {
			req.failureCount++
			return nil
		}},
		{From: requestFailed, To: requestFailed, Trigger: triggerReplicaSuccess, Action: func() error {
			req.successCount++
			return nil
		}},
		{From: requestFailed, To: requestFailed, Trigger: triggerReplicaFailure, Action: func() error {
			req.failureCount++
			return nil
		}},
		{From: requestAwaiting, To: requestSucceeded, Trigger: triggerQuorumReached, Action: func() error {
			c.reactor.CancelTimer(req.timerID)
			merged := mergeMaximalSiblings(req.responses)
			c.readRepairer.TriggerRepair(req.key, merged, req.responses)
			req.resolve(visibleSiblings(merged), nil)
			return nil
		}},
		{From: requestAwaiting, To: requestFailed, Trigger: triggerQuorumUnreachable, Action: func() error {
			c.reactor.CancelTimer(req.timerID)
			req.resolve(nil, newQuorumError("get", quorum, req.successCount))
			return nil
		}},
		{From: requestAwaiting, To: requestFailed, Trigger: triggerTimeout, Action: func() error {
			req.resolve(nil, newQuorumError("get", quorum, req.successCount))
			return nil
		}},
	})

	c.readRequests[req.id] = req
	req.timerID = c.reactor.ScheduleOnce(c.timeoutConfig.ClientTimeout, func() {
		c.onReadTimeout(req.id)
	})
	return req
}

func (c *Coordinator) onReadReplicaResult(reqID uint64, nodeID node.NodeID, ss *adapter.SiblingSet, err error) {
	req, ok := c.readRequests[reqID]
	if !ok {
		return
	}

	req.responses = append(req.responses, readrepair.ReplicaRead{NodeID: nodeID, SiblingSet: ss, Error: err})

	alreadyResolved := req.machine.State() != requestAwaiting
	if err == nil {
		_ = req.machine.Handle(triggerReplicaSuccess)
	} else {
		_ = req.machine.Handle(triggerReplicaFailure)
	}

	if !alreadyResolved {
		switch {
		case req.successCount >= req.quorum:
			_ = req.machine.Handle(triggerQuorumReached)
		case req.total-req.failureCount < req.quorum:
			_ = req.machine.Handle(triggerQuorumUnreachable)
		}
	}

	if req.successCount+req.failureCount >= req.total {
		delete(c.readRequests, req.id)
	}
}

func (c *Coordinator) onReadTimeout(reqID uint64) {
	req, ok := c.readRequests[reqID]
	if !ok || req.machine.State() != requestAwaiting {
		return
	}
	delete(c.readRequests, reqID)
	_ = req.machine.Handle(triggerTimeout)
}

func mergeMaximalSiblings(responses []readrepair.ReplicaRead) []adapter.Sibling {
	var all []adapter.Sibling
	for _, r := range responses {
		if r.Error != nil || r.SiblingSet == nil {
			continue
		}
		all = append(all, r.SiblingSet.Siblings...)
	}

	maximal := make([]adapter.Sibling, 0, len(all))
	for i, s := range all {
		dominated := false
		for j, other := range all {
			if i == j {
				continue
			}
			if other.VClock.Dominates(s.VClock) && !other.VClock.Equals(s.VClock) {
				dominated = true
				break
			}
		}
		if dominated {
			continue
		}
		duplicate := false
		for _, m := range maximal {
			if m.VClock.Equals(s.VClock) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			maximal = append(maximal, s)
		}
	}
	return maximal
}

func visibleSiblings(merged []adapter.Sibling) []adapter.Sibling {
	now := time.Now().Unix()
	visible := make([]adapter.Sibling, 0, len(merged))
	for _, s := range merged {
		if s.Tombstone {
			continue
		}
		if s.ExpiresAt != 0 && s.ExpiresAt <= now {
			continue
		}
		visible = append(visible, s)
	}
	return visible
}

func newQuorumError(op string, required, achieved int) error {
	return &quorumerr.QuorumError{
		Type:      quorumerr.QuorumNotReached,
		Required:  required,
		Achieved:  achieved,
		Operation: op,
	}
}
