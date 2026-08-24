package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/antientropy"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/failuredetector"
	"goquorum.io/v2/engine/gossip"
	"goquorum.io/v2/engine/handoff"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/readrepair"
	"goquorum.io/v2/infra/reactor"
)

// PutOptions carries per-request write tuning for a Put.
type PutOptions struct {
	TTLSeconds int64 // 0 = no TTL; >0 = key expires this many seconds from now.
}

// Coordinator is the central brain orchestrating quorum reads/writes, cluster membership,
// peer failure detection, gossip dissemination, hinted handoff, and anti-entropy.
// All public methods dispatch onto the reactor goroutine via PostFunc.
type Coordinator struct {
	nodeID     node.NodeID
	ring       *hashring.HashRing
	storage    adapter.Storage
	transport  adapter.ClientTransport
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
	failureDetector *failuredetector.FailureDetector
	gossip          *gossip.Gossip
	handoff         *handoff.HintedHandoff
	antiEntropy     *antientropy.AntiEntropy

	peerFSM *PeerFSM
	started bool
	stopped bool

	heartbeatTimer   reactor.TimerID
	gossipTimer      reactor.TimerID
	handoffTimer     reactor.TimerID
	antiEntropyTimer reactor.TimerID

	requestSeq    uint64
	writeRequests map[uint64]*writeRequest
	readRequests  map[uint64]*readRequest
}

// Option modifies Coordinator configuration parameters.
type Option func(*Coordinator)

// WithFailureDetectorConfig sets failure detector parameters.
func WithFailureDetectorConfig(cfg config.FailureDetectorConfig) Option {
	return func(c *Coordinator) { c.failureDetectorConfig = cfg }
}

// WithAntiEntropyConfig sets anti-entropy parameters.
func WithAntiEntropyConfig(cfg config.AntiEntropyConfig) Option {
	return func(c *Coordinator) { c.antiEntropyConfig = cfg }
}

// WithReadRepairConfig sets read-repair parameters.
func WithReadRepairConfig(cfg config.ReadRepairConfig) Option {
	return func(c *Coordinator) { c.readRepairConfig = cfg }
}

// WithGossipConfig sets gossip parameters.
func WithGossipConfig(cfg gossip.GossipConfig, interval time.Duration) Option {
	return func(c *Coordinator) {
		c.gossipConfig = cfg
		c.gossipInterval = interval
	}
}

// WithHandoffInterval sets hinted handoff replay interval.
func WithHandoffInterval(interval time.Duration) Option {
	return func(c *Coordinator) { c.handoffInterval = interval }
}

// WithTimeoutConfig sets request timeout parameters.
func WithTimeoutConfig(cfg config.TimeoutConfig) Option {
	return func(c *Coordinator) { c.timeoutConfig = cfg }
}

// NewCoordinator constructs a coordinator attached to storage, transport, ring, and reactor.
func NewCoordinator(
	id node.NodeID,
	ring *hashring.HashRing,
	store adapter.Storage,
	tr adapter.ClientTransport,
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
	}

	for _, opt := range opts {
		opt(c)
	}

	c.readRepairer = readrepair.NewReadRepairer(id, tr, c.readRepairConfig)
	c.antiEntropy = antientropy.NewAntiEntropy(id, store, ring, tr, c.antiEntropyConfig)
	c.failureDetector = failuredetector.NewFailureDetector(tr, c)
	c.gossip = gossip.NewGossip(tr, c, c.gossipConfig)
	c.handoff = handoff.NewHintedHandoff(tr, id)

	c.peerFSM = NewPeerFSM(c.failureDetectorConfig.FailureThreshold, c.onPeerTransition)

	if mm != nil {
		for _, p := range mm.GetAllPeers() {
			c.peerFSM.AddPeer(p, node.NodeStateActive)
			if p != id {
				addr := mm.GetHTTPAddress(p)
				if addr != "" {
					_ = tr.Dial(p, addr)
				}
			}
		}
	}

	if h, ok := tr.(interface {
		SetHandler(adapter.ClientAdapterHandler)
	}); ok {
		h.SetHandler(c)
	}

	return c
}

// HandleCompletion delegates completion demuxing to the underlying transport if supported.
func (c *Coordinator) HandleCompletion(ev reactor.Event) bool {
	if h, ok := c.transport.(interface{ HandleCompletion(reactor.Event) bool }); ok {
		return h.HandleCompletion(ev)
	}
	return false
}

// Start starts background anti-entropy sync, dials known peers, and arms master reactor timers.
func (c *Coordinator) Start() error {
	if c.started {
		return nil
	}
	c.started = true

	if c.membership != nil {
		for _, p := range c.membership.GetPeers() {
			if p.ID != c.nodeID {
				addr := c.membership.GetHTTPAddress(p.ID)
				if addr != "" {
					_ = c.transport.Dial(p.ID, addr)
				}
			}
		}
	}

	if err := c.antiEntropy.Build(); err != nil {
		return err
	}
	c.armTimers()
	return nil
}

// Stop stops background timers and subsystems, and disposes the outbound client transport.
func (c *Coordinator) Stop() {
	if !c.started || c.stopped {
		return
	}
	c.stopped = true
	c.disarmTimers()
	if c.transport != nil {
		_ = c.transport.Close()
	}
}

// IsRunning reports whether the coordinator has been started and not yet stopped.
func (c *Coordinator) IsRunning() bool {
	return c.started && !c.stopped
}

func (c *Coordinator) onPeerTransition(id node.NodeID, from, to node.NodeState) {
	switch to {
	case node.NodeStateActive:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(id, membership.NodeStatusActive)
		}
		_ = c.ring.UpdateNodeState(id, node.NodeStateActive)
		if from == node.NodeStateFailed && c.handoff != nil {
			c.handoff.Replay([]node.NodeID{id})
		}
	case node.NodeStateDegraded:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(id, membership.NodeStatusSuspect)
		}
		_ = c.ring.UpdateNodeState(id, node.NodeStateDegraded)
	case node.NodeStateFailed:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(id, membership.NodeStatusFailed)
		}
		_ = c.ring.UpdateNodeState(id, node.NodeStateFailed)
	case node.NodeStateLeaving:
		if c.membership != nil {
			c.membership.UpdatePeerStatus(id, membership.NodeStatusLeaving)
		}
		_ = c.ring.UpdateNodeState(id, node.NodeStateLeaving)
	default:
		panic("unhandled default case")
	}
}

// OnHeartbeatResult implements failuredetector.ProbeHandler.
func (c *Coordinator) OnHeartbeatResult(nodeID node.NodeID, err error) {
	c.peerFSM.OnHeartbeatResult(nodeID, err)
}

// OnGossipReceived implements gossip.GossipHandler.
func (c *Coordinator) OnGossipReceived(peerID node.NodeID, entries []adapter.GossipEntry) {
	if c.membership != nil {
		for _, entry := range entries {
			if entry.NodeID != c.nodeID {
				c.membership.UpdatePeerStatus(entry.NodeID, membership.NodeStatus(entry.Status))
			}
		}
	}
	c.peerFSM.OnGossipReceived(entries, c.nodeID)
}

func (c *Coordinator) armTimers() {
	if c.failureDetector != nil && c.failureDetectorConfig.HeartbeatInterval > 0 {
		c.heartbeatTimer = c.reactor.ScheduleEvery(c.failureDetectorConfig.HeartbeatInterval, func() {
			c.failureDetector.Probe(c.getPeerIDs())
		})
	}
	if c.gossip != nil && c.gossipInterval > 0 {
		c.gossipTimer = c.reactor.ScheduleEvery(c.gossipInterval, func() {
			c.gossip.Round(c.getPeerIDs(), c.GetLocalGossipEntries())
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

// GetLocalGossipEntries returns this node's view of gossip entries.
func (c *Coordinator) GetLocalGossipEntries() []adapter.GossipEntry {
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

	req := newWriteRequest(c.nextRequestID(), len(prefList), c.quorumConfig.W, func(err error) {
		if err != nil {
			done(vclock.VectorClock{}, err)
			return
		}
		done(tick, nil)
	})
	c.writeRequests[req.id] = req
	req.timerID = c.reactor.ScheduleOnce(c.timeoutConfig.ClientTimeout, func() {
		c.onWriteTimeout(req.id, "put")
	})

	keyBytes := []byte(key)
	for _, nodeID := range prefList {
		if nodeID == c.nodeID {
			reqID := req.id
			c.storage.Put(keyBytes, siblingSet, func(err error) {
				c.onWriteReplicaResult(reqID, err, "put")
			})
		} else {
			_ = c.transport.RemotePut(nodeID, req.id, keyBytes, siblingSet)
		}
	}
}

func (c *Coordinator) onWriteReplicaResult(reqID uint64, err error, op string) {
	req, ok := c.writeRequests[reqID]
	if !ok {
		return
	}
	req.handleResult(err, op, c.reactor.CancelTimer)
	if req.isDone() {
		delete(c.writeRequests, reqID)
	}
}

func (c *Coordinator) onWriteTimeout(reqID uint64, op string) {
	req, ok := c.writeRequests[reqID]
	if !ok || req.state != requestAwaiting {
		return
	}
	delete(c.writeRequests, reqID)
	req.handleTimeout(op, c.reactor.CancelTimer)
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
	req := newReadRequest(c.nextRequestID(), keyBytes, len(prefList), c.quorumConfig.R, done)
	c.readRequests[req.id] = req
	req.timerID = c.reactor.ScheduleOnce(c.timeoutConfig.ClientTimeout, func() {
		c.onReadTimeout(req.id)
	})

	for _, nodeID := range prefList {
		if nodeID == c.nodeID {
			reqID, nid := req.id, nodeID
			c.storage.Get(keyBytes, func(ss *adapter.SiblingSet, err error) {
				c.onReadReplicaResult(reqID, nid, ss, err)
			})
		} else {
			_ = c.transport.RemoteGet(nodeID, req.id, keyBytes)
		}
	}
}

func (c *Coordinator) onReadReplicaResult(reqID uint64, nodeID node.NodeID, ss *adapter.SiblingSet, err error) {
	req, ok := c.readRequests[reqID]
	if !ok {
		return
	}
	req.handleResult(nodeID, ss, err, c.readRepairer.TriggerRepair, c.reactor.CancelTimer)
	if req.isDone() {
		delete(c.readRequests, reqID)
	}
}

func (c *Coordinator) onReadTimeout(reqID uint64) {
	req, ok := c.readRequests[reqID]
	if !ok || req.state != requestAwaiting {
		return
	}
	delete(c.readRequests, reqID)
	req.handleTimeout(c.readRepairer.TriggerRepair, c.reactor.CancelTimer)
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

	req := newWriteRequest(c.nextRequestID(), len(prefList), c.quorumConfig.W, done)
	c.writeRequests[req.id] = req
	req.timerID = c.reactor.ScheduleOnce(c.timeoutConfig.ClientTimeout, func() {
		c.onWriteTimeout(req.id, "delete")
	})

	keyBytes := []byte(key)
	for _, nodeID := range prefList {
		if nodeID == c.nodeID {
			reqID := req.id
			c.storage.Put(keyBytes, siblingSet, func(err error) {
				c.onWriteReplicaResult(reqID, err, "delete")
			})
		} else {
			_ = c.transport.RemotePut(nodeID, req.id, keyBytes, siblingSet)
		}
	}
}

// GetMerkleRoot returns the coordinator's current anti-entropy Merkle root.
func (c *Coordinator) GetMerkleRoot() []byte {
	return c.antiEntropy.GetMerkleRoot()
}

var _ adapter.ClientAdapterHandler = (*Coordinator)(nil)

// OnRemotePutResponse handles a write replication response from peerID.
func (c *Coordinator) OnRemotePutResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode) {
	c.onWriteReplicaResult(corrID, wire.StatusCodeToError(status), "put")
}

// OnRemoteGetResponse handles a read replication response from peerID.
func (c *Coordinator) OnRemoteGetResponse(peerID node.NodeID, corrID uint64, siblings *adapter.SiblingSet, status wire.StatusCode) {
	c.onReadReplicaResult(corrID, peerID, siblings, wire.StatusCodeToError(status))
}

// OnHeartbeatResponse handles a heartbeat ping response from peerID.
func (c *Coordinator) OnHeartbeatResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode) {
	c.OnHeartbeatResult(peerID, wire.StatusCodeToError(status))
}

// OnGetMerkleRootResponse handles a Merkle root response from peerID.
func (c *Coordinator) OnGetMerkleRootResponse(peerID node.NodeID, corrID uint64, root []byte, status wire.StatusCode) {
	if c.antiEntropy != nil {
		c.antiEntropy.OnMerkleRootResult(peerID, root, wire.StatusCodeToError(status))
	}
}

// OnNotifyLeavingResponse handles a graceful leaving response from peerID.
func (c *Coordinator) OnNotifyLeavingResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode) {
}

// OnGossipExchangeResponse handles a gossip state digest response from peerID.
func (c *Coordinator) OnGossipExchangeResponse(peerID node.NodeID, corrID uint64, entries []adapter.GossipEntry) {
	c.OnGossipReceived(peerID, entries)
}

// OnPeerConnected is invoked when an outbound connection to peerID succeeds.
func (c *Coordinator) OnPeerConnected(peerID node.NodeID, addr string) {
	c.OnHeartbeatResult(peerID, nil)
}

// OnPeerDisconnected is invoked when an outbound connection to peerID is dropped.
func (c *Coordinator) OnPeerDisconnected(peerID node.NodeID, err error) {
	c.OnHeartbeatResult(peerID, err)
}

// OnPeerConnectError is invoked when an outbound connection attempt to peerID fails.
func (c *Coordinator) OnPeerConnectError(peerID node.NodeID, err error) {
	c.OnHeartbeatResult(peerID, err)
}

// OnRPCError is invoked when an outbound RPC times out or suffers a transport error.
func (c *Coordinator) OnRPCError(peerID node.NodeID, corrID uint64, rpcType uint16, err error) {
	switch rpcType {
	case uint16(wire.MsgRemotePutRequest):
		c.onWriteReplicaResult(corrID, err, "put")
	case uint16(wire.MsgRemoteGetRequest):
		c.onReadReplicaResult(corrID, peerID, nil, err)
	case uint16(wire.MsgHeartbeatRequest):
		c.OnHeartbeatResult(peerID, err)
	case uint16(wire.MsgGetMerkleRootRequest):
		if c.antiEntropy != nil {
			c.antiEntropy.OnMerkleRootResult(peerID, nil, err)
		}
	}
}
