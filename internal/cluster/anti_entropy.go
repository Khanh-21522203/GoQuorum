package cluster

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
	"context"
	"fmt"
	"sync"
	"time"
)

// AntiEntropy manages background replica synchronization (Section 4)
type AntiEntropy struct {
	nodeID  common.NodeID
	storage *storage.Storage
	ring    *HashRing
	rpc     RPCClient

	// Merkle tree for efficient divergence detection
	merkleTree *MerkleTree

	// Configuration
	config  config.AntiEntropyConfig
	metrics *AntiEntropyMetrics

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewAntiEntropy creates anti-entropy service
func NewAntiEntropy(
	nodeID common.NodeID,
	storage *storage.Storage,
	ring *HashRing,
	rpc RPCClient,
	config config.AntiEntropyConfig) *AntiEntropy {

	return &AntiEntropy{
		nodeID:     nodeID,
		storage:    storage,
		ring:       ring,
		rpc:        rpc,
		merkleTree: NewMerkleTree(config.MerkleDepth), // HERE!
		config:     config,
		metrics:    NewAntiEntropyMetrics(),
		stopCh:     make(chan struct{}),
	}
}

// Start starts anti-entropy background process (Section 4.5)
func (ae *AntiEntropy) Start() error {
	if !ae.config.Enabled {
		fmt.Println("Anti-entropy disabled")
		return nil
	}

	fmt.Println("Starting anti-entropy service...")

	// Build initial Merkle tree
	fmt.Println("Building initial Merkle tree...")
	if err := ae.merkleTree.Build(ae.storage); err != nil {
		return fmt.Errorf("failed to build merkle tree: %w", err)
	}
	fmt.Printf("Merkle tree built: root=%x\n", ae.merkleTree.GetRoot()[:8])

	// Start scheduler
	ae.wg.Add(1)
	go ae.schedulerLoop()

	return nil
}

// Stop stops anti-entropy service
func (ae *AntiEntropy) Stop() {
	close(ae.stopCh)
	ae.wg.Wait()
	fmt.Println("Anti-entropy service stopped")
}

// GetMerkleRoot returns the current Merkle tree root hash
func (ae *AntiEntropy) GetMerkleRoot() []byte {
	return ae.merkleTree.GetRoot()
}

// schedulerLoop runs periodic synchronization (Section 4.5)
func (ae *AntiEntropy) schedulerLoop() {
	defer ae.wg.Done()

	// Calculate interval per range to spread work evenly
	numRanges := ae.config.NumBuckets() // 2^MerkleDepth buckets, must match merkle tree
	intervalPerRange := ae.config.ScanInterval / time.Duration(numRanges)

	ticker := time.NewTicker(intervalPerRange)
	defer ticker.Stop()

	rangeIdx := 0

	for {
		select {
		case <-ticker.C:
			// Exchange one range per tick
			ae.exchangeRange(rangeIdx)

			rangeIdx = (rangeIdx + 1) % numRanges

		case <-ae.stopCh:
			return
		}
	}
}

// exchangeRange synchronizes one key range with peers (Section 4.4)
func (ae *AntiEntropy) exchangeRange(rangeIdx int) {
	start := time.Now()
	defer func() {
		ae.metrics.ScanDuration.Observe(time.Since(start).Seconds())
	}()

	// Get peers responsible for this range
	// For MVP: exchange with all peers
	peers := ae.ring.GetAllNodes()

	for _, peerID := range peers {
		if peerID == ae.nodeID {
			continue // Skip self
		}

		// Exchange with this peer
		ae.metrics.ExchangesTotal.Inc()

		err := ae.exchangeWithPeer(peerID, rangeIdx)
		if err != nil {
			ae.metrics.ExchangesFailed.Inc()
			fmt.Printf("Anti-entropy exchange failed with %s: %v\n", peerID, err)
		}
	}
}

// exchangeWithPeer exchanges data with single peer (Section 4.4)
func (ae *AntiEntropy) exchangeWithPeer(peerID common.NodeID, rangeIdx int) error {
	ctx, cancel := context.WithTimeout(context.Background(), ae.config.ExchangeTimeout)
	defer cancel()

	// Step 1: Compare root hashes
	myRoot := ae.merkleTree.GetRoot()

	peerRoot, err := ae.rpc.GetMerkleRoot(ctx, peerID)
	if err != nil {
		return fmt.Errorf("failed to get peer root: %w", err)
	}

	// If roots match, no divergence
	if bytesEqual(myRoot, peerRoot) {
		return nil // In sync
	}

	fmt.Printf("Divergence detected with %s in range %d, performing key exchange\n", peerID, rangeIdx)

	// Step 2: Scan keys belonging to this bucket and push them to the peer.
	// Keys are mapped to buckets via keyToBucket. We push every key whose
	// bucket == rangeIdx so the peer can merge and win-via-vector-clock.
	numKeys := 0

	err = ae.storage.Scan(nil, nil, func(key []byte, siblings *storage.SiblingSet) bool {
		if ae.merkleTree.keyToBucket(key) != rangeIdx {
			return true // Not in this bucket, skip
		}

		// Push the key to the peer
		pushCtx, pushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pushCancel()

		if putErr := ae.rpc.RemotePut(pushCtx, peerID, key, siblings); putErr != nil {
			fmt.Printf("Anti-entropy: failed to push key %x to %s: %v\n", key, peerID, putErr)
		} else {
			numKeys++
		}

		return true
	})

	if err != nil {
		return fmt.Errorf("scan for anti-entropy exchange: %w", err)
	}

	if numKeys > 0 {
		ae.metrics.KeysExchanged.Add(float64(numKeys))
	}

	return nil
}

// TriggerWithPeer immediately starts a full anti-entropy exchange with a specific
// peer in a background goroutine.  Called when a node recovers from failure so
// diverged data is reconciled right away instead of waiting for the next
// scheduled scan interval (default 1 h).
func (ae *AntiEntropy) TriggerWithPeer(nodeID common.NodeID) {
	if !ae.config.Enabled {
		return
	}
	ae.wg.Add(1)
	go func() {
		defer ae.wg.Done()
		fmt.Printf("Post-recovery anti-entropy triggered with %s\n", nodeID)
		numBuckets := ae.config.NumBuckets()
		for i := 0; i < numBuckets; i++ {
			select {
			case <-ae.stopCh:
				return
			default:
				if err := ae.exchangeWithPeer(nodeID, i); err != nil {
					fmt.Printf("Post-recovery anti-entropy error with %s bucket %d: %v\n", nodeID, i, err)
				}
			}
		}
	}()
}

// SyncWithPeers performs a full synchronous anti-entropy exchange with every
// peer in the list.  Unlike TriggerWithPeer (which is async), this blocks
// until all buckets have been pushed to all peers.  Used during node leave so
// data is fully drained before the node is removed from the ring.
func (ae *AntiEntropy) SyncWithPeers(peers []common.NodeID) {
	if !ae.config.Enabled {
		return
	}
	numBuckets := ae.config.NumBuckets()
	for _, peer := range peers {
		fmt.Printf("[REBALANCE] Draining data to %s (%d buckets)...\n", peer, numBuckets)
		for i := 0; i < numBuckets; i++ {
			select {
			case <-ae.stopCh:
				return
			default:
				if err := ae.exchangeWithPeer(peer, i); err != nil {
					fmt.Printf("[REBALANCE] Drain to %s bucket %d: %v\n", peer, i, err)
				}
			}
		}
		fmt.Printf("[REBALANCE] Drain to %s complete\n", peer)
	}
}

// OnKeyUpdate updates Merkle tree when key changes (Section 4.3 - Incremental)
func (ae *AntiEntropy) OnKeyUpdate(key []byte, siblings *storage.SiblingSet) {
	if !ae.config.Enabled {
		return
	}

	// Incremental update (very fast - O(1))
	ae.merkleTree.UpdateKey(key, siblings)
}

// OnKeyDelete updates Merkle tree when key deleted
func (ae *AntiEntropy) OnKeyDelete(key []byte, oldSiblings *storage.SiblingSet) {
	if !ae.config.Enabled {
		return
	}

	ae.merkleTree.RemoveKey(key, oldSiblings)
}
