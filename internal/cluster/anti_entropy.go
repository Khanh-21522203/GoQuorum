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

// schedulerLoop runs periodic synchronization (Section 4.5)
func (ae *AntiEntropy) schedulerLoop() {
	defer ae.wg.Done()

	// Calculate interval per range to spread work evenly
	numRanges := 256 // Partition key space
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

	// Step 2: Find differing ranges (would traverse tree in full implementation)
	// For MVP: Simple approach - exchange all keys in range

	// Step 3: Exchange keys (simplified)
	// In production, would use Merkle tree traversal to find exact differences

	fmt.Printf("Divergence detected with %s in range %d\n", peerID, rangeIdx)

	return nil
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
