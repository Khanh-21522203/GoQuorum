package antientropy

import (
	"bytes"
	"errors"
	"fmt"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/statemachine"
)

// ErrReactorNotSet is returned by Start when no reactor.Reactor has been
// attached via SetReactor yet, so there is nothing to schedule the
// periodic scan round on.
var ErrReactorNotSet = errors.New("antientropy: reactor not set, call SetReactor before Start")

// lifecycleState is AntiEntropy's own run state. It is tracked separately
// from the Merkle tree's contents because the tree can legitimately be
// queried and incrementally updated (GetMerkleRoot, OnKeyUpdate,
// OnKeyDelete) before Start has ever run, or after Stop has run — those
// calls only ever touch merkleTree directly and never consult this
// machine.
type lifecycleState int

const (
	// lifecycleIdle is the state before Start has succeeded, and the
	// state Start falls back to if Build fails, so a later Start call
	// can retry.
	lifecycleIdle lifecycleState = iota
	// lifecycleBuilding covers exactly the span of Start's synchronous
	// call to merkleTree.Build. It exists as its own state (rather than
	// folding the build into the idle -> running transition) so that a
	// second Start call arriving before the first one's Build returns is
	// rejected by the table instead of silently double-building.
	lifecycleBuilding
	// lifecycleRunning is the steady state: the tree is built and the
	// scan timer is armed.
	lifecycleRunning
	// lifecycleStopped is terminal; Stop does not support being
	// restarted via Start (matching the fixed-table design of
	// statemachine.Machine which operates on immutable, acyclic-by-convention
	// transitions).
	lifecycleStopped
)

// lifecycleTrigger drives lifecycleState transitions.
type lifecycleTrigger int

const (
	lifecycleTriggerStart          lifecycleTrigger = iota
	lifecycleTriggerBuildSucceeded                  // building -> running
	lifecycleTriggerBuildFailed                     // building -> idle (retryable)
	lifecycleTriggerStop                            // running -> stopped
)

func newLifecycle() *statemachine.Machine[lifecycleState, lifecycleTrigger] {
	return statemachine.New(lifecycleIdle, []statemachine.Edge[lifecycleState, lifecycleTrigger]{
		{From: lifecycleIdle, Trigger: lifecycleTriggerStart, To: lifecycleBuilding},
		{From: lifecycleBuilding, Trigger: lifecycleTriggerBuildSucceeded, To: lifecycleRunning},
		{From: lifecycleBuilding, Trigger: lifecycleTriggerBuildFailed, To: lifecycleIdle},
		{From: lifecycleRunning, Trigger: lifecycleTriggerStop, To: lifecycleStopped},
	})
}

// AntiEntropy runs the background Merkle-tree reconciliation process: it
// periodically compares the local Merkle root against each peer's and
// resyncs any peer whose root has drifted, and keeps the tree updated
// incrementally as keys are written or deleted.
type AntiEntropy struct {
	nodeID     node.NodeID
	storage    adapter.Storage
	ring       *hashring.HashRing
	transport  adapter.Transport
	merkleTree *MerkleTree
	config     config.AntiEntropyConfig

	reactor     *reactor.Reactor
	lifecycle   *statemachine.Machine[lifecycleState, lifecycleTrigger]
	scanTimerID reactor.TimerID
}

// NewAntiEntropy creates an anti-entropy runner for the local node. The
// Merkle tree is allocated immediately (empty) so GetMerkleRoot,
// OnKeyUpdate, and OnKeyDelete are safe to call right away; Start performs
// the real initial scan that populates it. Call SetReactor before Start.
func NewAntiEntropy(nodeID node.NodeID, store adapter.Storage, ring *hashring.HashRing, tr adapter.Transport, cfg config.AntiEntropyConfig) *AntiEntropy {
	return &AntiEntropy{
		nodeID:     nodeID,
		storage:    store,
		ring:       ring,
		transport:  tr,
		merkleTree: NewMerkleTree(cfg.MerkleDepth),
		config:     cfg,
		lifecycle:  newLifecycle(),
	}
}

// SetReactor attaches the reactor.Reactor that Start uses to schedule the
// periodic scan round. It must be called before Start.
//
// Reactor injection happens through a setter rather than a constructor
// parameter (unlike some sibling subsystems in this module) because
// NewAntiEntropy already has a call site that predates reactor wiring in
// this codebase; forcing a reactor through the constructor today would
// require that call site to fabricate one before it is ready to actually
// run it. A setter keeps that call site compiling and defers the real
// wiring to whichever change gives it a reactor of its own.
func (ae *AntiEntropy) SetReactor(r *reactor.Reactor) {
	ae.reactor = r
}

// Start builds the initial Merkle tree and arms the periodic scan timer.
// It is a no-op returning nil if config.Enabled is false, and a no-op
// returning nil if Start has already succeeded (or is presently building)
// on a prior call — the underlying lifecycle machine's invalid-transition
// rejection is what makes the second call inert rather than a separate
// boolean flag. If the initial build fails, the error is returned, the
// timer is not armed, and the lifecycle resets to idle so a later Start
// call may retry.
func (ae *AntiEntropy) Start() error {
	if !ae.config.Enabled {
		return nil
	}
	if ae.reactor == nil {
		return ErrReactorNotSet
	}
	if err := ae.lifecycle.Handle(lifecycleTriggerStart); err != nil {
		// Already building, running, or stopped: treat as an idempotent
		// no-op rather than surfacing the rejection to the caller.
		return nil
	}

	if err := ae.merkleTree.Build(ae.storage); err != nil {
		_ = ae.lifecycle.Handle(lifecycleTriggerBuildFailed)
		return fmt.Errorf("antientropy: build merkle tree: %w", err)
	}

	if err := ae.lifecycle.Handle(lifecycleTriggerBuildSucceeded); err != nil {
		return err
	}
	ae.scanTimerID = ae.reactor.ScheduleEvery(ae.config.ScanInterval, ae.scanTick)
	return nil
}

// Stop cancels the scan timer and halts future scan rounds. It is a no-op
// if called before Start has reached the running state, or more than
// once.
func (ae *AntiEntropy) Stop() {
	if err := ae.lifecycle.Handle(lifecycleTriggerStop); err != nil {
		return
	}
	ae.reactor.CancelTimer(ae.scanTimerID)
}

// GetMerkleRoot returns the current Merkle root hash.
func (ae *AntiEntropy) GetMerkleRoot() []byte {
	return ae.merkleTree.GetRoot()
}

// scanTick runs one scan round: every other node currently on the ring is
// checked for divergence and, if found, resynced. Peers are visited
// sequentially rather than fanned out with config.Parallelism, since every
// call into transport is already non-blocking — it returns immediately and
// resumes later via its done callback — so there is no thread of execution
// for Parallelism to bound. Concurrent backpressure only matters for
// operations that occupy a goroutine while waiting, which this
// reactor-driven engine never does.
func (ae *AntiEntropy) scanTick() {
	for _, n := range ae.ring.Nodes() {
		if n.ID == ae.nodeID {
			continue
		}
		ae.TriggerWithPeer(n.ID)
	}
}

// TriggerWithPeer runs a Merkle exchange with a single peer: it fetches
// the peer's current root and, if it differs from the local root, resyncs
// that peer. It is fire-and-forget — the actual work happens later, in the
// transport's done callback, once the reactor delivers the response.
//
// The exchange is a root-hash comparison followed by a full local-keyspace
// resync, not a bucket-level diff. transport.Transport exposes only a
// single root hash per peer (GetMerkleRoot) and a per-key write
// (RemotePut); there is no RPC to fetch a peer's tree levels or bucket
// contents, so a divergence found here cannot be narrowed down to the
// specific diverging bucket range the way MerkleTree.Compare narrows a
// divergence between two local trees. Re-pushing every local key is the
// closest resync obtainable from those two primitives alone; it is safe to
// repeat because the peer's own vector-clock reconciliation on Put makes
// re-pushing an already-in-sync key a no-op.
func (ae *AntiEntropy) TriggerWithPeer(nodeID node.NodeID) {
	if !ae.config.Enabled {
		return
	}
	localRoot := ae.merkleTree.GetRoot()
	ae.transport.GetMerkleRoot(nodeID, func(peerRoot []byte, err error) {
		if err != nil {
			// Best-effort: the next scheduled round (or another
			// TriggerWithPeer call) will retry.
			return
		}
		if bytes.Equal(localRoot, peerRoot) {
			return
		}
		ae.pushAllKeysTo(nodeID, func(error) {})
	})
}

// SyncWithPeers drains the full local keyspace to every given peer,
// unconditionally (unlike TriggerWithPeer, it does not check root
// divergence first) — used when the local node is leaving the cluster and
// must hand off everything it holds regardless of whether a peer already
// has a copy. done is invoked exactly once, after every peer's push has
// completed, with the first error encountered across all peers and keys
// (or nil if none failed). Every push that has already started is allowed
// to finish even after a failure is observed elsewhere, rather than being
// abandoned, since a partial resync is strictly worse than a slightly
// slower complete one.
func (ae *AntiEntropy) SyncWithPeers(peers []node.NodeID, done func(error)) {
	if !ae.config.Enabled || len(peers) == 0 {
		done(nil)
		return
	}

	remaining := len(peers)
	var firstErr error
	settleOne := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
		remaining--
		if remaining == 0 {
			done(firstErr)
		}
	}

	for _, peer := range peers {
		ae.pushAllKeysTo(peer, settleOne)
	}
}

// pushAllKeysTo scans the full local keyspace and pushes every key to peer
// via RemotePut, invoking done exactly once with the first error
// encountered (from the scan itself or from any individual push), or nil
// if every push succeeded. RemotePut's done callback may fire either
// synchronously (as fakes in tests do) or later from the reactor loop (as
// a real network transport does), so completion is tracked with an
// outstanding-push counter rather than assumed to happen inside the Scan
// call: done only fires once the scan has finished walking keys AND every
// push it started has reported back.
func (ae *AntiEntropy) pushAllKeysTo(peer node.NodeID, done func(error)) {
	pending := 0
	scanFinished := false
	settled := false
	var firstErr error

	maybeFinish := func() {
		if settled || !scanFinished || pending > 0 {
			return
		}
		settled = true
		done(firstErr)
	}
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	ae.storage.Scan(nil, nil, func(key []byte, siblings *adapter.SiblingSet) bool {
		pending++
		ae.transport.RemotePut(peer, key, siblings, func(err error) {
			recordErr(err)
			pending--
			maybeFinish()
		})
		return true
	}, func(err error) {
		recordErr(err)
		scanFinished = true
		maybeFinish()
	})
}

// OnKeyUpdate incrementally folds a write for key into the Merkle tree.
func (ae *AntiEntropy) OnKeyUpdate(key []byte, siblings *adapter.SiblingSet) {
	if !ae.config.Enabled {
		return
	}
	ae.merkleTree.UpdateKey(key, siblings)
}

// OnKeyDelete incrementally removes a deleted key's prior contribution
// from the Merkle tree.
func (ae *AntiEntropy) OnKeyDelete(key []byte, oldSiblings *adapter.SiblingSet) {
	if !ae.config.Enabled {
		return
	}
	ae.merkleTree.RemoveKey(key, oldSiblings)
}
