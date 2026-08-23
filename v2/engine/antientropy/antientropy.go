package antientropy

import (
	"bytes"
	"fmt"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
)

// AntiEntropy reconciles Merkle trees with peers to repair partition drift.
//
// Exchange Flow:
//
//	ScanTick(peers) ──> For each peer:
//	                      transport.GetMerkleRoot(peer)
//	                            │
//	                            ├── Equal     ──> In sync (no-op)
//	                            └── Different ──> Diverged: pushAllKeysTo(peer)
type AntiEntropy struct {
	nodeID     node.NodeID
	storage    adapter.Storage
	ring       *hashring.HashRing
	transport  adapter.Transport
	merkleTree *MerkleTree
	config     config.AntiEntropyConfig
}

// NewAntiEntropy creates an anti-entropy runner for the local node.
func NewAntiEntropy(nodeID node.NodeID, store adapter.Storage, ring *hashring.HashRing, tr adapter.Transport, cfg config.AntiEntropyConfig) *AntiEntropy {
	return &AntiEntropy{
		nodeID:     nodeID,
		storage:    store,
		ring:       ring,
		transport:  tr,
		merkleTree: NewMerkleTree(cfg.MerkleDepth),
		config:     cfg,
	}
}

// Build scans the local storage and populates the initial Merkle tree.
func (ae *AntiEntropy) Build() error {
	if !ae.config.Enabled {
		return nil
	}
	if err := ae.merkleTree.Build(ae.storage); err != nil {
		return fmt.Errorf("antientropy: build merkle tree: %w", err)
	}
	return nil
}

// GetMerkleRoot returns the current Merkle root hash.
func (ae *AntiEntropy) GetMerkleRoot() []byte {
	return ae.merkleTree.GetRoot()
}

// ScanTick executes a single anti-entropy scan round across the given peers.
func (ae *AntiEntropy) ScanTick(peerIDs []node.NodeID) {
	if !ae.config.Enabled {
		return
	}
	for _, id := range peerIDs {
		if id == ae.nodeID {
			continue
		}
		ae.TriggerWithPeer(id)
	}
}

// TriggerWithPeer runs a Merkle exchange with a single peer.
func (ae *AntiEntropy) TriggerWithPeer(nodeID node.NodeID) {
	if !ae.config.Enabled {
		return
	}
	localRoot := ae.merkleTree.GetRoot()
	ae.transport.GetMerkleRoot(nodeID, func(peerRoot []byte, err error) {
		if err != nil || bytes.Equal(localRoot, peerRoot) {
			return
		}
		ae.pushAllKeysTo(nodeID, func(error) {})
	})
}

// SyncWithPeers drains the entire local keyspace to the given peers.
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

// OnKeyDelete incrementally removes a deleted key's prior contribution from the Merkle tree.
func (ae *AntiEntropy) OnKeyDelete(key []byte, oldSiblings *adapter.SiblingSet) {
	if !ae.config.Enabled {
		return
	}
	ae.merkleTree.RemoveKey(key, oldSiblings)
}
