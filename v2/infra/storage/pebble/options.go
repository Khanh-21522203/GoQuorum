package pebble

import (
	"time"

	"goquorum.io/v2/contracts/node"
)

// Options configures the Pebble-backed storage engine. Fields carry yaml
// tags because infra/config assembles this struct from the loaded
// configuration (see infra/config.StorageConfig.Pebble).
//
// (v1: internal/storage/options.go StorageOptions)
type Options struct {
	// Basic config.
	DataDir               string      `yaml:"data_dir"`
	NodeID                node.NodeID `yaml:"node_id"`
	SyncWrites            bool        `yaml:"sync_writes"`            // Default: true.
	BlockCacheSize        int64       `yaml:"block_cache_size"`       // Bytes. Default: 256MB.
	MemTableSize          int         `yaml:"memtable_size"`          // Bytes. Default: 64MB.
	MaxOpenFiles          int         `yaml:"max_open_files"`         // Default: 1000.
	CompactionConcurrency int         `yaml:"compaction_concurrency"` // Default: 1.

	// Sibling management.
	MaxKeySize              int `yaml:"max_key_size"`              // Bytes. Default: 65536 (64KB).
	MaxValueSize            int `yaml:"max_value_size"`            // Bytes. Default: 1048576 (1MB).
	MaxSiblings             int `yaml:"max_siblings"`              // Default: 100.
	SiblingWarningThreshold int `yaml:"sibling_warning_threshold"` // Default: 10.

	// Vector clock pruning.
	VClockPruneThreshold time.Duration `yaml:"vclock_prune_threshold"` // Default: 7 days.
	VClockMaxEntries     int           `yaml:"vclock_max_entries"`     // Default: 50.

	// Tombstone GC.
	TombstoneGCEnabled  bool          `yaml:"tombstone_gc_enabled"`
	TombstoneTTL        time.Duration `yaml:"tombstone_ttl"`         // Default: 7 days.
	TombstoneGCInterval time.Duration `yaml:"tombstone_gc_interval"` // Default: 1 hour.
}
