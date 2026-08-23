package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter/storage"
	"goquorum.io/v2/engine/adapter/transport"
	"goquorum.io/v2/engine/antientropy"
	"goquorum.io/v2/engine/config"
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

// coordinatorState is the Coordinator's own start/stop lifecycle.
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

// requestState is the lifecycle of a single in-flight Put/Get/Delete's
// quorum resolution.
type requestState int

const (
	requestAwaiting  requestState = iota // Still waiting on replica responses.
	requestSucceeded                     // Quorum reached; the caller's done has fired.
	requestFailed                        // Quorum became unreachable, or the client timeout fired.
)

// requestTrigger is the set of events that drive a per-request machine.
type requestTrigger int

const (
	triggerReplicaSuccess    requestTrigger = iota // A replica call succeeded.
	triggerReplicaFailure                          // A replica call failed.
	triggerQuorumReached                           // Enough successes to satisfy the request.
	triggerQuorumUnreachable                       // Too many failures for quorum to still be possible.
	triggerTimeout                                 // The client timeout fired first.
)

// writeRequest tracks one in-flight Put or Delete's replication fan-out.
//
// The entry is kept in Coordinator.writeRequests until every replica this
// request contacted has reported back (success or failure), even though
// resolve may already have fired once quorum was reached: stragglers still
// update successCount/failureCount for observability, they just can no
// longer change the outcome the caller already saw.
type writeRequest struct {
	id           uint64
	total        int // Number of replicas contacted.
	quorum       int // Successes required (W).
	successCount int
	failureCount int
	resolve      func(error) // Invoked exactly once, with nil on success.
	timerID      reactor.TimerID
	machine      *statemachine.Machine[requestState, requestTrigger]
}

// readRequest tracks one in-flight Get's replication fan-out.
type readRequest struct {
	id           uint64
	key          []byte
	total        int
	quorum       int // Successes required (R).
	successCount int
	failureCount int
	responses    []readrepair.ReplicaRead // Every response collected so far, in arrival order.
	resolve      func([]storage.Sibling, error)
	timerID      reactor.TimerID
	machine      *statemachine.Machine[requestState, requestTrigger]
}

// Coordinator orchestrates quorum reads and writes across a key's
// preference list of replicas, composing the hash ring, membership view,
// read-repair, and anti-entropy subsystems on top of the storage.Storage
// and transport.Transport ports.
//
// Every exported method bounces onto reactor's single goroutine via
// PostFunc before touching any Coordinator-owned state, so the maps below
// need no mutex even though Put/Get/Delete may be called from arbitrary
// caller goroutines.
type Coordinator struct {
	nodeID     node.NodeID
	ring       *hashring.HashRing
	storage    storage.Storage
	transport  transport.Transport
	membership *membership.MembershipManager
	reactor    *reactor.Reactor

	quorumConfig     config.QuorumConfig
	readRepairConfig config.ReadRepairConfig
	timeoutConfig    config.TimeoutConfig

	readRepairer *readrepair.ReadRepairer
	antiEntropy  *antientropy.AntiEntropy

	lifecycle *statemachine.Machine[coordinatorState, coordinatorTrigger]

	requestSeq    uint64
	writeRequests map[uint64]*writeRequest
	readRequests  map[uint64]*readRequest
}

// NewCoordinator constructs a coordinator over the given ports (storage,
// transport), hash ring, and membership view, driven by rt, applying cfg
// as the N/R/W quorum configuration. Read-repair and anti-entropy tuning
// default to config.DefaultReadRepairConfig/DefaultAntiEntropyConfig/
// DefaultTimeoutConfig.
func NewCoordinator(
	id node.NodeID,
	ring *hashring.HashRing,
	store storage.Storage,
	tr transport.Transport,
	mm *membership.MembershipManager,
	rt *reactor.Reactor,
	cfg config.QuorumConfig,
) *Coordinator {
	readRepairConfig := config.DefaultReadRepairConfig()
	antiEntropyConfig := config.DefaultAntiEntropyConfig()

	c := &Coordinator{
		nodeID:           id,
		ring:             ring,
		storage:          store,
		transport:        tr,
		membership:       mm,
		reactor:          rt,
		quorumConfig:     cfg,
		readRepairConfig: readRepairConfig,
		timeoutConfig:    config.DefaultTimeoutConfig(),
		readRepairer:     readrepair.NewReadRepairer(id, tr, readRepairConfig),
		antiEntropy:      antientropy.NewAntiEntropy(id, store, ring, tr, antiEntropyConfig),
		writeRequests:    make(map[uint64]*writeRequest),
		readRequests:     make(map[uint64]*readRequest),
	}
	c.antiEntropy.SetReactor(rt)

	c.lifecycle = statemachine.New(coordinatorNotStarted, []statemachine.Edge[coordinatorState, coordinatorTrigger]{
		{From: coordinatorNotStarted, To: coordinatorRunning, Trigger: coordinatorTriggerStart, Action: func() error {
			return c.antiEntropy.Start()
		}},
		{From: coordinatorRunning, To: coordinatorStopped, Trigger: coordinatorTriggerStop, Action: func() error {
			c.antiEntropy.Stop()
			return nil
		}},
	})

	return c
}

// Start starts the coordinator's background subsystems (anti-entropy).
func (c *Coordinator) Start() error {
	return c.lifecycle.Handle(coordinatorTriggerStart)
}

// Stop stops the coordinator's background subsystems.
func (c *Coordinator) Stop() {
	_ = c.lifecycle.Handle(coordinatorTriggerStop)
}

// nextRequestID hands out a locally-incrementing ID for a new in-flight
// request. Only ever called from the reactor goroutine, so a plain counter
// is enough.
func (c *Coordinator) nextRequestID() uint64 {
	c.requestSeq++
	return c.requestSeq
}

// Put performs a quorum write of value under key, causally ordered by
// causal, and reports the resulting vector clock through done.
func (c *Coordinator) Put(key string, value []byte, causal vclock.VectorClock, done func(vclock.VectorClock, error), opts ...PutOptions) {
	c.reactor.PostFunc(func() {
		c.doPut(key, value, causal, done, opts...)
	})
}

func (c *Coordinator) doPut(key string, value []byte, causal vclock.VectorClock, done func(vclock.VectorClock, error), opts ...PutOptions) {
	// Copy before ticking: causal's underlying map is shared with the
	// caller's own copy, and mutating it in place would be a visible side
	// effect on a value the caller still holds.
	tick := causal.Copy()
	tick.Tick(c.nodeID)

	var expiresAt int64
	if len(opts) > 0 && opts[0].TTLSeconds > 0 {
		expiresAt = time.Now().Unix() + opts[0].TTLSeconds
	}

	siblingSet := &storage.SiblingSet{
		Siblings: []storage.Sibling{{
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
		cb := func(err error) { c.onWriteReplicaResult(reqID, err) }
		if nodeID == c.nodeID {
			c.storage.Put(keyBytes, siblingSet, cb)
		} else {
			c.transport.RemotePut(nodeID, keyBytes, siblingSet, cb)
		}
	}
}

// Get performs a quorum read of key, merging sibling sets from R replicas
// and triggering read repair on stale replicas.
func (c *Coordinator) Get(key string, done func([]storage.Sibling, error)) {
	c.reactor.PostFunc(func() {
		c.doGet(key, done)
	})
}

func (c *Coordinator) doGet(key string, done func([]storage.Sibling, error)) {
	prefList, err := c.ring.GetPreferenceList(key, c.quorumConfig.N)
	if err != nil {
		done(nil, err)
		return
	}

	keyBytes := []byte(key)
	req := c.newReadRequest(keyBytes, len(prefList), c.quorumConfig.R, done)

	for _, nodeID := range prefList {
		reqID, nid := req.id, nodeID
		cb := func(ss *storage.SiblingSet, err error) { c.onReadReplicaResult(reqID, nid, ss, err) }
		if nodeID == c.nodeID {
			c.storage.Get(keyBytes, cb)
		} else {
			c.transport.RemoteGet(nodeID, keyBytes, cb)
		}
	}
}

// Delete performs a quorum tombstone write for key, causally ordered by
// causal.
func (c *Coordinator) Delete(key string, causal vclock.VectorClock, done func(error)) {
	c.reactor.PostFunc(func() {
		c.doDelete(key, causal, done)
	})
}

func (c *Coordinator) doDelete(key string, causal vclock.VectorClock, done func(error)) {
	tick := causal.Copy()
	tick.Tick(c.nodeID)

	siblingSet := &storage.SiblingSet{
		Siblings: []storage.Sibling{{
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
		cb := func(err error) { c.onWriteReplicaResult(reqID, err) }
		if nodeID == c.nodeID {
			c.storage.Put(keyBytes, siblingSet, cb)
		} else {
			c.transport.RemotePut(nodeID, keyBytes, siblingSet, cb)
		}
	}
}

// GetMerkleRoot returns the coordinator's current anti-entropy Merkle
// root.
func (c *Coordinator) GetMerkleRoot() []byte {
	return c.antiEntropy.GetMerkleRoot()
}

// newWriteRequest builds and registers the per-request machine for a Put
// or Delete's fan-out, and arms its client timeout.
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
		// Stragglers arriving after the request has already resolved still
		// update the tallies; they just have no edge back to a terminal
		// state, so they can never re-fire resolve.
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

// onWriteReplicaResult processes one replica's Put/Delete response.
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
			// TODO(v2): sloppy quorum overflow — when c.quorumConfig.SloppyQuorum
			// is set, retry via extended preference list overflow nodes before
			// giving up. Not handled in this pass; strict quorum only.
			_ = req.machine.Handle(triggerQuorumUnreachable)
		}
	}

	if req.successCount+req.failureCount >= req.total {
		delete(c.writeRequests, req.id)
	}
}

// onWriteTimeout fires when a Put/Delete's client timeout elapses before
// quorum was reached. The request is removed immediately: no per-replica
// timeout exists in this pass, so a straggler past this point could
// otherwise pin the entry in the map indefinitely.
func (c *Coordinator) onWriteTimeout(reqID uint64) {
	req, ok := c.writeRequests[reqID]
	if !ok || req.machine.State() != requestAwaiting {
		return
	}
	delete(c.writeRequests, reqID)
	_ = req.machine.Handle(triggerTimeout)
}

// newReadRequest builds and registers the per-request machine for a Get's
// fan-out, and arms its client timeout.
func (c *Coordinator) newReadRequest(key []byte, total, quorum int, resolve func([]storage.Sibling, error)) *readRequest {
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

// onReadReplicaResult processes one replica's Get response.
func (c *Coordinator) onReadReplicaResult(reqID uint64, nodeID node.NodeID, ss *storage.SiblingSet, err error) {
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

// onReadTimeout fires when a Get's client timeout elapses before quorum
// was reached.
func (c *Coordinator) onReadTimeout(reqID uint64) {
	req, ok := c.readRequests[reqID]
	if !ok || req.machine.State() != requestAwaiting {
		return
	}
	delete(c.readRequests, reqID)
	_ = req.machine.Handle(triggerTimeout)
}

// mergeMaximalSiblings reconciles siblings collected from multiple
// replicas down to the causally maximal ones: a sibling is dropped only if
// some other sibling's vector clock strictly dominates it. What survives
// is either a single value (all replicas agreed, or one strictly
// superseded the rest) or several concurrent siblings that the caller must
// resolve itself — the standard sibling-exposing behavior for an
// eventually-consistent store. Tombstones and expired entries are kept
// here so the dominance math sees the whole picture; callers that want the
// user-visible view should filter with visibleSiblings.
func mergeMaximalSiblings(responses []readrepair.ReplicaRead) []storage.Sibling {
	var all []storage.Sibling
	for _, r := range responses {
		if r.Error != nil || r.SiblingSet == nil {
			continue
		}
		all = append(all, r.SiblingSet.Siblings...)
	}

	maximal := make([]storage.Sibling, 0, len(all))
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

// visibleSiblings filters tombstones and TTL-expired siblings out of a
// merged set, for the value actually returned to a Get caller.
func visibleSiblings(merged []storage.Sibling) []storage.Sibling {
	now := time.Now().Unix()
	visible := make([]storage.Sibling, 0, len(merged))
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

// newQuorumError reports that a quorum operation could not collect enough
// successful replica responses.
func newQuorumError(op string, required, achieved int) error {
	return &quorumerr.QuorumError{
		Type:      quorumerr.QuorumNotReached,
		Required:  required,
		Achieved:  achieved,
		Operation: op,
	}
}
