package storage

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/vclock"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

// Storage implements the persistence layer using Pebble
type Storage struct {
	db      *pebble.DB
	nodeID  common.NodeID
	opts    StorageOptions
	metrics *StorageMetrics

	// Lifecycle
	closed  sync.Once
	closeCh chan struct{}
	wg      sync.WaitGroup
}

// SiblingSet represents multiple concurrent versions (Section 7.1)
type SiblingSet struct {
	Siblings []Sibling
}

// Sibling represents one version of a key (Section 7.1)
type Sibling struct {
	Value     []byte
	VClock    vclock.VectorClock
	Timestamp int64 // Unix timestamp
	Tombstone bool
}

// NewStorage creates a new Pebble-backed storage (Section 7.2)
func NewStorage(opts StorageOptions) (*Storage, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	// Configure Pebble (Section 7.2)
	pebbleOpts := &pebble.Options{
		// Memory management (Section 6)
		Cache:                       pebble.NewCache(opts.BlockCacheSize),
		MemTableSize:                uint64(opts.MemTableSize),
		MemTableStopWritesThreshold: 4,

		// Compaction (Section 5)
		L0CompactionThreshold: 4,
		L0StopWritesThreshold: 12,
		LBaseMaxBytes:         64 << 20, // 64MB
		MaxConcurrentCompactions: func() int {
			return opts.CompactionConcurrency
		},

		// Durability (Section 4.1)
		DisableWAL: !opts.SyncWrites,

		// Limits
		MaxOpenFiles: opts.MaxOpenFiles,
	}

	db, err := pebble.Open(opts.DataDir, pebbleOpts)
	if err != nil {
		return nil, fmt.Errorf("open pebble: %w", err)
	}

	s := &Storage{
		db:      db,
		nodeID:  opts.NodeID,
		opts:    opts,
		metrics: NewStorageMetrics(),
		closeCh: make(chan struct{}),
	}

	// Start background tasks
	if opts.TombstoneGCEnabled {
		s.wg.Add(1)
		go s.tombstoneGCLoop()
	}

	return s, nil
}

func (s *Storage) LocalNodeID() common.NodeID { return s.nodeID }

func (s *Storage) Get(key []byte) (*SiblingSet, error) {
	start := time.Now()
	defer func() {
		s.metrics.ReadLatency.Observe(time.Since(start).Seconds())
	}()

	if s.isClosed() {
		return nil, common.ErrStorageClosed
	}

	if err := ValidateKey(key); err != nil {
		return nil, fmt.Errorf("invalid key: %w", err)
	}

	// Read from Pebble (Section 2.3 - Read Path)
	data, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, common.ErrKeyNotFound
	}
	if err != nil {
		s.metrics.ReadErrors.Inc()
		return nil, fmt.Errorf("pebble get: %w", err)
	}
	defer closer.Close()

	s.metrics.BytesRead.Add(float64(len(data)))

	// Decode (Section 3.3 - Value Encoding)
	siblings, err := decodeSiblingSet(data)
	if err != nil {
		s.metrics.CorruptedReads.Inc()
		return nil, fmt.Errorf("decode: %w", err)
	}

	// Filter expired tombstones (client-side for Get)
	filteredSiblings := s.filterTombstones(siblings.Siblings)

	return &SiblingSet{Siblings: filteredSiblings}, nil
}

// Put stores siblings for a key with vector clock reconciliation (Section 7.1)
func (s *Storage) Put(key []byte, siblings *SiblingSet) error {
	start := time.Now()
	defer func() {
		s.metrics.WriteLatency.Observe(time.Since(start).Seconds())
	}()

	if s.isClosed() {
		return common.ErrStorageClosed
	}

	if err := ValidateKey(key); err != nil {
		return fmt.Errorf("invalid key: %w", err)
	}

	for i, sib := range siblings.Siblings {
		if err := ValidateValue(sib.Value); err != nil {
			return fmt.Errorf("invalid value at sibling %d: %w", i, err)
		}
	}

	// Read existing siblings
	existing, err := s.Get(key)
	if err != nil && err != common.ErrKeyNotFound {
		return fmt.Errorf("read existing: %w", err)
	}

	// Merge with existing using vector clock reconciliation
	merged := s.reconcileSiblings(existing, siblings)

	for i := range merged.Siblings {
		pruned := merged.Siblings[i].VClock.Prune(
			s.opts.VClockPruneThreshold,
			s.opts.VClockMaxEntries,
		)
		if pruned > 0 {
			s.metrics.VClockEntriesPruned.Add(float64(pruned))
		}
	}

	// Enforce sibling limits (Section 6.3 from Consistency Model)
	if len(merged.Siblings) > s.opts.MaxSiblings {
		merged.Siblings = s.pruneSiblings(merged.Siblings, s.opts.MaxSiblings)
		s.metrics.SiblingsPruned.Inc()
	}

	if len(merged.Siblings) > s.opts.SiblingWarningThreshold {
		s.metrics.SiblingExplosion.Inc()
		fmt.Printf("WARNING: Key has %d siblings (threshold: %d)\n",
			len(merged.Siblings), s.opts.SiblingWarningThreshold)
	}

	// Encode (Section 3.3)
	data, err := encodeSiblingSet(merged)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	s.metrics.BytesWritten.Add(float64(len(data)))

	// Write to Pebble (Section 2.2 - Write Path)
	writeOpts := pebble.Sync
	if !s.opts.SyncWrites {
		writeOpts = pebble.NoSync
	}

	if err := s.db.Set(key, data, writeOpts); err != nil {
		s.metrics.WriteErrors.Inc()
		// Check for disk full (Section 8.2)
		if isDiskFullError(err) {
			s.metrics.DiskFullErrors.Inc()
			return fmt.Errorf("%w: %v", common.ErrStorageFull, err)
		}

		// Check for I/O error (Section 8.1)
		if isIOError(err) {
			s.metrics.IOErrors.Inc()
			return fmt.Errorf("%w: %v", common.ErrStorageIO, err)
		}

		return fmt.Errorf("pebble set: %w", err)
	}

	s.metrics.KeysTotal.Inc()
	return nil
}

// Delete creates a tombstone (Section 3.4 & 7.1)
func (s *Storage) Delete(key []byte, vclock vclock.VectorClock) error {
	tombstone := Sibling{
		Value:     []byte{}, // Empty value
		VClock:    vclock,
		Timestamp: time.Now().Unix(),
		Tombstone: true,
	}

	return s.Put(key, &SiblingSet{Siblings: []Sibling{tombstone}})
}

// Scan iterates keys in range [start, end) (Section 7.1)
type ScanFunc func(key []byte, siblings *SiblingSet) bool

func (s *Storage) Scan(start, end []byte, fn ScanFunc) error {
	if s.isClosed() {
		return common.ErrStorageClosed
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: start,
		UpperBound: end,
	})
	if err != nil {
		return fmt.Errorf("create iterator: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := append([]byte(nil), iter.Key()...)

		siblings, err := decodeSiblingSet(iter.Value())
		if err != nil {
			// Log error but continue iteration
			fmt.Printf("ERROR: Failed to decode key %x: %v\n", key, err)
			continue
		}

		// Call user function
		if !fn(key, siblings) {
			break // Stop iteration
		}
	}

	return iter.Error()
}

// reconcileSiblings merges new siblings with existing using vector clocks
func (s *Storage) reconcileSiblings(existing, incoming *SiblingSet) *SiblingSet {
	if existing == nil || len(existing.Siblings) == 0 {
		return incoming
	}
	if incoming == nil || len(incoming.Siblings) == 0 {
		return existing
	}

	merged := make([]Sibling, 0, len(existing.Siblings)+len(incoming.Siblings))

	// Add all incoming siblings
	merged = append(merged, incoming.Siblings...)

	// Add existing siblings that are not obsolete
	for _, existingSib := range existing.Siblings {
		obsolete := false

		// Check if any incoming sibling supersedes this one
		for _, incomingSib := range incoming.Siblings {
			if incomingSib.VClock.HappensAfter(existingSib.VClock) {
				obsolete = true
				break
			}
		}

		if !obsolete {
			merged = append(merged, existingSib)
		}
	}

	// Deduplicate by vector clock
	return &SiblingSet{Siblings: s.dedupSiblings(merged)}
}

// dedupSiblings removes duplicate siblings based on vector clock
func (s *Storage) dedupSiblings(siblings []Sibling) []Sibling {
	seen := make(map[string]bool)
	result := make([]Sibling, 0, len(siblings))

	for _, sib := range siblings {
		key := sib.VClock.String()
		if !seen[key] {
			seen[key] = true
			result = append(result, sib)
		}
	}

	return result
}

// pruneSiblings keeps only the newest siblings (by timestamp)
func (s *Storage) pruneSiblings(siblings []Sibling, maxCount int) []Sibling {
	if len(siblings) <= maxCount {
		return siblings
	}

	// Sort by timestamp descending (newest first)
	sorted := make([]Sibling, len(siblings))
	copy(sorted, siblings)

	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Timestamp > sorted[i].Timestamp {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted[:maxCount]
}

// filterTombstones removes tombstones from result (they exist for conflict resolution)
func (s *Storage) filterTombstones(siblings []Sibling) []Sibling {
	filtered := make([]Sibling, 0, len(siblings))
	for _, sib := range siblings {
		if !sib.Tombstone {
			filtered = append(filtered, sib)
		}
	}
	return filtered
}

// tombstoneGCLoop runs background GC for expired tombstones (Section 5.4)
func (s *Storage) tombstoneGCLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.opts.TombstoneGCInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			s.runTombstoneGC()
		}
	}
}

func (s *Storage) runTombstoneGC() {
	now := time.Now().Unix()
	ttl := int64(s.opts.TombstoneTTL.Seconds())

	iter, err := s.db.NewIter(nil)
	if err != nil {
		fmt.Printf("ERROR: Failed to create GC iterator: %v\n", err)
		return
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		siblings, err := decodeSiblingSet(iter.Value())
		if err != nil {
			continue
		}

		// Filter out expired tombstones
		filtered := make([]Sibling, 0, len(siblings.Siblings))
		gcCount := 0

		for _, sib := range siblings.Siblings {
			if sib.Tombstone && (now-sib.Timestamp) > ttl {
				gcCount++
				continue // Drop expired tombstone
			}
			filtered = append(filtered, sib)
		}

		// If we removed any tombstones, rewrite the key
		if gcCount > 0 {
			if len(filtered) == 0 {
				// All siblings were tombstones - delete key
				s.db.Delete(key, pebble.Sync)
			} else {
				// Rewrite with filtered siblings
				data, _ := encodeSiblingSet(&SiblingSet{Siblings: filtered})
				s.db.Set(key, data, pebble.Sync)
			}

			s.metrics.TombstonesGCed.Add(float64(gcCount))
		}
	}
}

// Close cleanly shuts down storage (Section 7.1)
func (s *Storage) Close() error {
	var err error
	s.closed.Do(func() {
		close(s.closeCh)
		s.wg.Wait()
		err = s.db.Close()
	})
	return err
}

func (s *Storage) isClosed() bool {
	select {
	case <-s.closeCh:
		return true
	default:
		return false
	}
}

// Stats returns storage statistics (Section 11)
func (s *Storage) Stats() StorageStats {
	metrics := s.db.Metrics()

	return StorageStats{
		KeyCount:        s.countKeys(),
		SizeBytes:       metrics.DiskSpaceUsage(),
		L0FileCount:     metrics.Levels[0].NumFiles,
		CompactionCount: metrics.Compact.Count,
		WALBytesWritten: metrics.WAL.BytesWritten,
	}
}

func (s *Storage) countKeys() int64 {
	var count int64
	iter, err := s.db.NewIter(nil)
	if err != nil {
		fmt.Printf("ERROR: Failed to create iterator for count: %v\n", err)
		return 0
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	return count
}

type StorageStats struct {
	KeyCount        int64
	SizeBytes       uint64
	L0FileCount     int64
	CompactionCount int64
	WALBytesWritten uint64
}

// Helper functions to detect error types
func isDiskFullError(err error) bool {
	// Check for ENOSPC
	return strings.Contains(err.Error(), "no space left on device") ||
		strings.Contains(err.Error(), "ENOSPC")
}

func isIOError(err error) bool {
	// Check for EIO
	return strings.Contains(err.Error(), "input/output error") ||
		strings.Contains(err.Error(), "EIO")
}
