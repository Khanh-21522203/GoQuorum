package storage

import (
	"GoQuorum/internal/common"
	"errors"
	"time"
)

// StorageOptions configures the storage engine (Section 10)
type StorageOptions struct {
	// Basic config (Section 10.1)
	DataDir               string
	NodeID                common.NodeID
	SyncWrites            bool  // Default: true (Section 4.1)
	BlockCacheSize        int64 // Default: 256MB
	MemTableSize          int   // Default: 64MB
	MaxOpenFiles          int   // Default: 1000
	CompactionConcurrency int   // Default: 1

	// Sibling management (Section 6.3 from Consistency Model)
	MaxKeySize   int // Default: 65536 (64 KB)
	MaxValueSize int // Default: 1048576 (1 MB)
	MaxSiblings  int // Default: 100

	// Sibling management
	SiblingWarningThreshold int // Default: 10

	// Vector clock pruning (Section 4.3)
	VClockPruneThreshold time.Duration // Default: 7 days
	VClockMaxEntries     int           // Default: 50

	// Tombstone GC (Section 10.2)
	TombstoneGCEnabled  bool
	TombstoneTTL        time.Duration // Default: 7 days
	TombstoneGCInterval time.Duration // Default: 1 hour
}

// DefaultStorageOptions returns default configuration (Section 10)
func DefaultStorageOptions(dataDir string, nodeID common.NodeID) StorageOptions {
	return StorageOptions{
		DataDir:               dataDir,
		NodeID:                nodeID,
		SyncWrites:            true,
		BlockCacheSize:        256 << 20, // 256MB
		MemTableSize:          64 << 20,  // 64MB
		MaxOpenFiles:          1000,
		CompactionConcurrency: 1,

		// Data limits (Section 11)
		MaxKeySize:   MaxKeySize,   // 65536
		MaxValueSize: MaxValueSize, // 1048576
		MaxSiblings:  100,

		SiblingWarningThreshold: 10,

		// Vector clock config (Section 4.3)
		VClockPruneThreshold: 7 * 24 * time.Hour, // 7 days
		VClockMaxEntries:     50,

		// Tombstone config (Section 6.4)
		TombstoneGCEnabled:  true,
		TombstoneTTL:        7 * 24 * time.Hour, // 7 days
		TombstoneGCInterval: 1 * time.Hour,
	}
}

// HighThroughputOptions for async writes (Section 10.3)
func HighThroughputOptions(dataDir string, nodeID common.NodeID) StorageOptions {
	opts := DefaultStorageOptions(dataDir, nodeID)
	opts.SyncWrites = false       // WARNING: may lose data on crash
	opts.BlockCacheSize = 1 << 30 // 1GB
	opts.MemTableSize = 128 << 20 // 128MB
	opts.CompactionConcurrency = 2
	return opts
}

// MemoryConstrainedOptions for low-memory systems (Section 10.3)
func MemoryConstrainedOptions(dataDir string, nodeID common.NodeID) StorageOptions {
	opts := DefaultStorageOptions(dataDir, nodeID)
	opts.BlockCacheSize = 64 << 20 // 64MB
	opts.MemTableSize = 32 << 20   // 32MB
	opts.MaxOpenFiles = 500
	return opts
}

func (o *StorageOptions) Validate() error {
	if o.DataDir == "" {
		return errors.New("data_dir is required")
	}
	if o.NodeID == "" {
		return errors.New("node_id is required")
	}
	if o.BlockCacheSize <= 0 {
		return errors.New("block_cache_size must be > 0")
	}
	if o.MemTableSize <= 0 {
		return errors.New("memtable_size must be > 0")
	}

	// Validate data limits (Section 2 & 3)
	if o.MaxKeySize <= 0 || o.MaxKeySize > MaxKeySize {
		return errors.New("max_key_size must be in (0, 65536]")
	}
	if o.MaxValueSize <= 0 || o.MaxValueSize > MaxValueSize {
		return errors.New("max_value_size must be in (0, 1048576]")
	}
	if o.MaxSiblings <= 0 {
		return errors.New("max_siblings must be > 0")
	}

	// Validate vector clock config (Section 4.3)
	if o.VClockPruneThreshold < 0 {
		return errors.New("vclock_prune_threshold must be >= 0")
	}
	if o.VClockMaxEntries <= 0 {
		return errors.New("vclock_max_entries must be > 0")
	}

	// Validate tombstone config (Section 6.4)
	if o.TombstoneTTL < 0 {
		return errors.New("tombstone_ttl must be >= 0")
	}

	return nil
}
