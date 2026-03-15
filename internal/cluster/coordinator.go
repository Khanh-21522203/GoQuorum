package cluster

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"time"
)

type Coordinator struct {
	nodeID        common.NodeID
	ring          *HashRing
	storage       *storage.Storage
	rpc           RPCClient
	membership    *MembershipManager
	readRepairer  *ReadRepairer
	antiEntropy   *AntiEntropy
	hintedHandoff *HintedHandoff

	// Configuration
	quorumConfig     config.QuorumConfig
	readRepairConfig config.ReadRepairConfig
	timeoutConfig    TimeoutConfig

	// Concurrency control (Section 7.1)
	keyLocks sync.Map // map[string]*sync.Mutex

	// In-flight request tracking for graceful shutdown
	inFlight atomic.Int64

	// Metrics (Section 9)
	metrics *CoordinatorMetrics
}

type TimeoutConfig struct {
	ClientTimeout  time.Duration // Default: 5s
	ReplicaTimeout time.Duration // Default: 2s
	RepairTimeout  time.Duration // Default: 1s
}

func NewCoordinator(
	nodeID common.NodeID,
	ring *HashRing,
	storage *storage.Storage,
	rpc RPCClient,
	membership *MembershipManager,
	quorumConfig config.QuorumConfig,
	readRepairConfig config.ReadRepairConfig,
	antiEntropyConfig config.AntiEntropyConfig) *Coordinator {

	readRepairer := NewReadRepairer(nodeID, rpc, readRepairConfig)
	antiEntropy := NewAntiEntropy(nodeID, storage, ring, rpc, antiEntropyConfig)

	return &Coordinator{
		nodeID:           nodeID,
		ring:             ring,
		storage:          storage,
		rpc:              rpc,
		membership:       membership,
		readRepairer:     readRepairer,
		antiEntropy:      antiEntropy,
		quorumConfig:     quorumConfig,
		readRepairConfig: readRepairConfig,
		timeoutConfig: TimeoutConfig{
			ClientTimeout:  5 * time.Second,
			ReplicaTimeout: 2 * time.Second,
			RepairTimeout:  1 * time.Second,
		},
		metrics: NewCoordinatorMetrics(),
	}
}

// Start starts coordinator and anti-entropy
func (c *Coordinator) Start() error {
	if c.hintedHandoff != nil {
		c.hintedHandoff.Start()
	}
	// Start anti-entropy background service
	return c.antiEntropy.Start()
}

// Stop stops coordinator and anti-entropy
func (c *Coordinator) Stop() {
	c.antiEntropy.Stop()
	if c.hintedHandoff != nil {
		c.hintedHandoff.Stop()
	}
}

// SetHintedHandoff attaches a HintedHandoff instance to this coordinator.
// Call this before Start() if hinted handoff is desired.
func (c *Coordinator) SetHintedHandoff(hh *HintedHandoff) {
	c.hintedHandoff = hh
}

// GetMerkleRoot returns the current Merkle tree root hash for anti-entropy.
func (c *Coordinator) GetMerkleRoot() []byte {
	return c.antiEntropy.GetMerkleRoot()
}

// InFlightCount returns the number of currently executing requests.
// Used by graceful shutdown to wait for in-flight requests to complete.
func (c *Coordinator) InFlightCount() int64 {
	return c.inFlight.Load()
}

// PutOptions holds optional parameters for a Put operation.
type PutOptions struct {
	TTLSeconds int64 // 0 = no TTL; >0 = key expires this many seconds from now
}

// Put writes key-value with W quorum (Section 3.1).
// An optional PutOptions may be passed to set TTL.
func (c *Coordinator) Put(ctx context.Context, key string, value []byte,
	context vclock.VectorClock, opts ...PutOptions) (vclock.VectorClock, error) {
	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)

	start := time.Now()
	defer func() {
		c.metrics.WriteLatency.Observe(time.Since(start).Seconds())
		c.metrics.PutRequestsTotal.Inc()
	}()

	keyBytes := []byte(key)

	// 1. Validate request (Section 3.1)
	if err := storage.ValidateKey(keyBytes); err != nil {
		return vclock.VectorClock{}, fmt.Errorf("invalid key: %w", err)
	}
	if err := storage.ValidateValue(value); err != nil {
		return vclock.VectorClock{}, fmt.Errorf("invalid value: %w", err)
	}

	// 2. Generate new vector clock (Section 3.1)
	newVClock := context.Copy()
	newVClock.Tick(c.nodeID)

	// 3. Build stored record (Section 3.1)
	var expiresAt int64
	if len(opts) > 0 && opts[0].TTLSeconds > 0 {
		expiresAt = time.Now().Unix() + opts[0].TTLSeconds
	}
	sibling := storage.Sibling{
		Value:     value,
		VClock:    newVClock,
		Timestamp: time.Now().Unix(), // Unix seconds; consistent with GC and tombstone timestamps
		Tombstone: false,
		ExpiresAt: expiresAt,
	}
	siblingSet := &storage.SiblingSet{
		Siblings: []storage.Sibling{sibling},
	}

	// 4. Get preference list (Section 2.2)
	prefList, err := c.ring.GetPreferenceList(key, c.quorumConfig.N)
	if err != nil {
		return vclock.VectorClock{}, fmt.Errorf("get preference list: %w", err)
	}

	// 5. Fan out writes (parallel) (Section 3.1)
	responses := c.parallelWrite(ctx, prefList, keyBytes, siblingSet)

	// 6. Collect and evaluate quorum (Section 3.1)
	successCount := 0
	replicaErrors := make([]common.ReplicaError, 0)

	for i, resp := range responses {
		if resp.Error == nil {
			successCount++
		} else {
			replicaErrors = append(replicaErrors, common.ReplicaError{
				NodeID: prefList[i],
				Error:  resp.Error,
			})
		}
	}

	// 7. Evaluate quorum (Section 3.1)
	if successCount >= c.quorumConfig.W {
		// Notify anti-entropy of key change (incremental Merkle tree update)
		go c.antiEntropy.OnKeyUpdate(keyBytes, siblingSet)

		c.metrics.WriteSuccess.Inc()
		return newVClock, nil // Success
	}

	// 7a. Sloppy quorum: try overflow nodes when strict quorum fails (Section 5.2)
	if c.quorumConfig.SloppyQuorum && successCount < c.quorumConfig.W {
		need := c.quorumConfig.W - successCount
		extList, extErr := c.ring.GetExtendedPreferenceList(key, c.quorumConfig.N, need)
		if extErr == nil && len(extList) > c.quorumConfig.N {
			// Identify overflow candidates (nodes beyond the strict preference list)
			strictSet := make(map[common.NodeID]struct{}, len(prefList))
			for _, nid := range prefList {
				strictSet[nid] = struct{}{}
			}
			overflowNodes := make([]common.NodeID, 0, need)
			for _, nid := range extList[c.quorumConfig.N:] {
				if _, inStrict := strictSet[nid]; !inStrict {
					overflowNodes = append(overflowNodes, nid)
				}
			}
			if len(overflowNodes) > 0 {
				overflowResps := c.parallelWrite(ctx, overflowNodes, keyBytes, siblingSet)
				for _, resp := range overflowResps {
					if resp.Error == nil {
						successCount++
					}
				}
			}
		}
	}

	if successCount >= c.quorumConfig.W {
		go c.antiEntropy.OnKeyUpdate(keyBytes, siblingSet)
		c.metrics.WriteSuccess.Inc()
		return newVClock, nil // Sloppy quorum success
	}

	// Quorum not reached (Section 3.4)
	c.metrics.WriteQuorumFailures.Inc()
	return vclock.VectorClock{}, &common.QuorumError{
		Type:          common.QuorumNotReached,
		Required:      c.quorumConfig.W,
		Achieved:      successCount,
		Operation:     "write",
		ReplicaErrors: replicaErrors,
	}
}

// parallelWrite fans out write to all N replicas (Section 3.1)
func (c *Coordinator) parallelWrite(
	ctx context.Context,
	prefList []common.NodeID,
	key []byte,
	siblings *storage.SiblingSet) []WriteResponse {

	// Buffer is sized for all replicas so goroutines never block on send.
	respChan := make(chan WriteResponse, len(prefList))

	var wg sync.WaitGroup
	// Fan out to all replicas concurrently
	for _, nodeID := range prefList {
		wg.Add(1)
		go func(nid common.NodeID) {
			defer wg.Done()
			writeCtx, cancel := context.WithTimeout(ctx, c.timeoutConfig.ReplicaTimeout)
			defer cancel()

			var err error

			// Section 8.1: Fast path for coordinator-is-replica
			if nid == c.nodeID {
				// Local write (no RPC)
				err = c.localWrite(key, siblings)
			} else {
				// Remote RPC call (Section 3.2)
				err = c.rpc.RemotePut(writeCtx, nid, key, siblings)
			}

			respChan <- WriteResponse{NodeID: nid, Error: err}
		}(nodeID)
	}

	// Collect all responses — buffered channel ensures goroutines never block.
	// We wait for all goroutines so no writes leak past the caller's lifetime.
	wg.Wait()

	responses := make([]WriteResponse, 0, len(prefList))
	for i := 0; i < len(prefList); i++ {
		responses = append(responses, <-respChan)
	}

	// Store hints for failed remote writes so they can be replayed once the
	// target node recovers (Section 5A: Hinted Handoff).
	if c.hintedHandoff != nil {
		for _, resp := range responses {
			if resp.Error != nil && resp.NodeID != c.nodeID {
				c.hintedHandoff.StoreHint(resp.NodeID, key, siblings) //nolint:errcheck
			}
		}
	}

	return responses
}

// localWrite performs local storage write with key locking (Section 3.3, 7.1)
func (c *Coordinator) localWrite(key []byte, siblings *storage.SiblingSet) error {
	// Per-key locking (Section 7.1)
	lock := c.getKeyLock(key)
	lock.Lock()
	defer lock.Unlock()

	// Write to local storage (Section 3.3)
	return c.storage.Put(key, siblings)
}

// getKeyLock returns lock for key (Section 7.1)
func (c *Coordinator) getKeyLock(key []byte) *sync.Mutex {
	keyStr := string(key)
	actual, _ := c.keyLocks.LoadOrStore(keyStr, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// Get reads key with R quorum (Section 4.1)
func (c *Coordinator) Get(ctx context.Context, key string) ([]storage.Sibling, error) {
	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)

	start := time.Now()
	defer func() {
		c.metrics.ReadLatency.Observe(time.Since(start).Seconds())
		c.metrics.GetRequestsTotal.Inc()
	}()

	keyBytes := []byte(key)

	// 1. Validate request (Section 4.1)
	if err := storage.ValidateKey(keyBytes); err != nil {
		return nil, fmt.Errorf("invalid key: %w", err)
	}

	// 2. Get preference list (Section 4.1)
	prefList, err := c.ring.GetPreferenceList(key, c.quorumConfig.N)
	if err != nil {
		return nil, fmt.Errorf("get preference list: %w", err)
	}

	// 3. Fan out reads to all N replicas (parallel) (Section 4.1)
	responses := c.parallelRead(ctx, prefList, keyBytes)

	// 4. Collect responses (Section 4.1)
	successCount := 0
	allSiblings := make([]storage.Sibling, 0)
	replicaErrors := make([]common.ReplicaError, 0)

	for i, resp := range responses {
		if resp.Error == nil {
			successCount++
			if resp.SiblingSet != nil {
				allSiblings = append(allSiblings, resp.SiblingSet.Siblings...)
			}
		} else {
			replicaErrors = append(replicaErrors, common.ReplicaError{
				NodeID: prefList[i],
				Error:  resp.Error,
			})
		}
	}

	// 5. Evaluate quorum (Section 4.1)
	if successCount < c.quorumConfig.R {
		c.metrics.ReadQuorumFailures.Inc()
		return nil, &common.QuorumError{
			Type:          common.QuorumNotReached,
			Required:      c.quorumConfig.R,
			Achieved:      successCount,
			Operation:     "read",
			ReplicaErrors: replicaErrors,
		}
	}

	// 6. Merge responses (Section 4.2)
	merged := c.mergeAllSiblings(allSiblings)

	// Record sibling count (Section 9.2)
	c.metrics.ReadSiblingsCount.Observe(float64(len(merged)))

	// 7. Check for read repair (Section 4.4)
	if c.readRepairConfig.Enabled {
		// Use ReadRepairer instead of inline logic
		if c.readRepairConfig.Async {
			go c.readRepairer.TriggerRepair(context.Background(), keyBytes, merged, responses)
		} else {
			c.readRepairer.TriggerRepair(ctx, keyBytes, merged, responses)
		}
	}

	// 8. Return result (Section 4.1)
	if len(merged) == 0 {
		return nil, common.ErrKeyNotFound // Section 4.3 Scenario 1
	}

	// Filter tombstones for client (Section 4.3)
	filtered := c.filterTombstones(merged)
	if len(filtered) == 0 {
		return nil, common.ErrKeyNotFound // Section 4.3 Scenario 2
	}

	c.metrics.ReadSuccess.Inc()
	return filtered, nil
}

// parallelRead fans out read to all N replicas (Section 4.1)
func (c *Coordinator) parallelRead(
	ctx context.Context,
	prefList []common.NodeID,
	key []byte) []ReadResponse {

	respChan := make(chan ReadResponse, len(prefList))

	for _, nodeID := range prefList {
		go func(nid common.NodeID) {
			readCtx, cancel := context.WithTimeout(ctx, c.timeoutConfig.ReplicaTimeout)
			defer cancel()

			var siblingSet *storage.SiblingSet
			var err error

			// Section 8.1: Fast path for coordinator-is-replica
			if nid == c.nodeID {
				// Local read — use GetRaw so tombstones are visible for read repair
				siblingSet, err = c.storage.GetRaw(key)
				if err == common.ErrKeyNotFound {
					err = nil // "not found" is a valid replica response
				}
			} else {
				// Remote RPC call
				siblingSet, err = c.rpc.RemoteGet(readCtx, nid, key)
			}

			select {
			case respChan <- ReadResponse{
				NodeID:     nid,
				SiblingSet: siblingSet,
				Error:      err,
			}:
			case <-readCtx.Done():
			}
		}(nodeID)
	}

	// Collect responses (Section 4.1)
	responses := make([]ReadResponse, 0, len(prefList))
	timeout := time.After(c.timeoutConfig.ReplicaTimeout)

	for i := 0; i < len(prefList); i++ {
		select {
		case resp := <-respChan:
			responses = append(responses, resp)

			// Early return after R successful reads (Section 8.3)
			successCount := 0
			for _, r := range responses {
				if r.Error == nil {
					successCount++
				}
			}

			if successCount >= c.quorumConfig.R {
				go drainChannel(respChan, len(prefList)-i-1)
				return responses
			}

		case <-timeout:
			go drainChannel(respChan, len(prefList)-i)
			return responses

		case <-ctx.Done():
			return responses
		}
	}

	return responses
}

// mergeAllSiblings merges siblings from multiple replicas (Section 4.2)
func (c *Coordinator) mergeAllSiblings(allSiblings []storage.Sibling) []storage.Sibling {
	if len(allSiblings) == 0 {
		return nil
	}

	// Find causally maximal siblings (Section 4.2)
	maximal := make([]storage.Sibling, 0)

	for _, s1 := range allSiblings {
		dominated := false

		for _, s2 := range allSiblings {
			// Skip self comparison
			if s1.VClock.Equals(s2.VClock) {
				continue
			}

			// Check if s1 is dominated by s2
			if s1.VClock.HappensBefore(s2.VClock) {
				dominated = true
				break
			}
		}

		if !dominated {
			// Deduplicate by vector clock
			found := false
			for _, existing := range maximal {
				if s1.VClock.Equals(existing.VClock) {
					found = true
					break
				}
			}
			if !found {
				maximal = append(maximal, s1)
			}
		}
	}

	return maximal
}

// filterTombstones removes tombstones from result (Section 4.3)
func (c *Coordinator) filterTombstones(siblings []storage.Sibling) []storage.Sibling {
	filtered := make([]storage.Sibling, 0, len(siblings))
	for _, sib := range siblings {
		if !sib.Tombstone {
			filtered = append(filtered, sib)
		}
	}
	return filtered
}

// Delete creates a tombstone with W quorum (Section 5.1)
func (c *Coordinator) Delete(ctx context.Context, key string, context vclock.VectorClock) error {
	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)

	c.metrics.DeleteRequestsTotal.Inc()

	// Validate context is provided (Section 5.1)
	if context.IsEmpty() {
		return fmt.Errorf("context required for delete")
	}

	// Generate tombstone record (Section 5.1)
	newVClock := context.Copy()
	newVClock.Tick(c.nodeID)

	tombstone := storage.Sibling{
		Value:     []byte{}, // Empty value
		VClock:    newVClock,
		Timestamp: time.Now().Unix(), // Use Unix seconds so GC can compare correctly
		Tombstone: true,
	}

	siblingSet := &storage.SiblingSet{
		Siblings: []storage.Sibling{tombstone},
	}

	// Execute as normal write (Section 5.1)
	keyBytes := []byte(key)
	prefList, err := c.ring.GetPreferenceList(key, c.quorumConfig.N)
	if err != nil {
		return fmt.Errorf("get preference list: %w", err)
	}

	// Read old value for Merkle tree removal (use GetRaw to see tombstones)
	oldSiblings, _ := c.storage.GetRaw(keyBytes)

	responses := c.parallelWrite(ctx, prefList, keyBytes, siblingSet)

	successCount := countSuccesses(responses)
	if successCount >= c.quorumConfig.W {
		// Notify anti-entropy of key deletion
		go c.antiEntropy.OnKeyDelete(keyBytes, oldSiblings)

		return nil
	}

	return &common.QuorumError{
		Type:      common.QuorumNotReached,
		Required:  c.quorumConfig.W,
		Achieved:  successCount,
		Operation: "delete",
	}
}

// BatchGetResult holds the result of a single key lookup in a batch.
type BatchGetResult struct {
	Key      string
	Siblings []storage.Sibling
	Error    error
}

// BatchPutItem holds a single write item for a batch put.
type BatchPutItem struct {
	Key     string
	Value   []byte
	Context vclock.VectorClock
}

// BatchPutResult holds the result of a single write in a batch.
type BatchPutResult struct {
	Key        string
	NewContext vclock.VectorClock
	Error      error
}

// BatchGet reads multiple keys concurrently.
func (c *Coordinator) BatchGet(ctx context.Context, keys []string) []BatchGetResult {
	results := make([]BatchGetResult, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func(idx int, k string) {
			defer wg.Done()
			siblings, err := c.Get(ctx, k)
			results[idx] = BatchGetResult{Key: k, Siblings: siblings, Error: err}
		}(i, key)
	}
	wg.Wait()
	return results
}

// BatchPut writes multiple keys concurrently.
func (c *Coordinator) BatchPut(ctx context.Context, items []BatchPutItem) []BatchPutResult {
	results := make([]BatchPutResult, len(items))
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(idx int, it BatchPutItem) {
			defer wg.Done()
			newVC, err := c.Put(ctx, it.Key, it.Value, it.Context)
			results[idx] = BatchPutResult{Key: it.Key, NewContext: newVC, Error: err}
		}(i, item)
	}
	wg.Wait()
	return results
}

type WriteResponse struct {
	NodeID common.NodeID
	Error  error
}

type ReadResponse struct {
	NodeID     common.NodeID
	SiblingSet *storage.SiblingSet
	Error      error
}

type Versioned struct {
	Value  []byte
	VClock vclock.VectorClock
}

// helpers
// drainChannel reads remaining responses from channel without blocking
// Used when we already have enough responses (early return)
func drainChannel[T any](ch chan T, remaining int) {
	for i := 0; i < remaining; i++ {
		select {
		case <-ch:
			// Discard response
		case <-time.After(100 * time.Millisecond):
			// Don't wait forever
			return
		}
	}
}

// countSuccesses counts successful responses
func countSuccesses(responses []WriteResponse) int {
	count := 0
	for _, resp := range responses {
		if resp.Error == nil {
			count++
		}
	}
	return count
}
