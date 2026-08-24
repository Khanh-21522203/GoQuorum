package config

import (
	"time"

	"goquorum.io/v2/contracts/node"
	pebblestore "goquorum.io/v2/infra/storage/pebble"
)

// StorageConfig defines storage settings as loaded from YAML, in
// human-friendly units (MB, days). Pebble converts it into
// infra/storage/pebble.Options, which uses bytes and time.Duration.
//
// (v1: internal/config/storage.go StorageConfig)
type StorageConfig struct {
	SyncWrites   bool `yaml:"sync_writes"`
	CacheSizeMB  int  `yaml:"cache_size_mb"`
	MemTableMB   int  `yaml:"memtable_mb"`
	MaxOpenFiles int  `yaml:"max_open_files"`

	MaxKeySizeKB            int `yaml:"max_key_size_kb"`
	MaxValueSizeMB          int `yaml:"max_value_size_mb"`
	MaxSiblings             int `yaml:"max_siblings"`
	SiblingWarningThreshold int `yaml:"sibling_warning_threshold"`

	TombstoneGCEnabled  bool          `yaml:"tombstone_gc_enabled"`
	TombstoneTTLDays    int           `yaml:"tombstone_ttl_days"`
	TombstoneGCInterval time.Duration `yaml:"tombstone_gc_interval"`

	VClockPruneDays  int `yaml:"vclock_prune_days"`
	VClockMaxEntries int `yaml:"vclock_max_entries"`
}

// Pebble converts the YAML storage config into infra/storage/pebble.Options
// for the given node and data directory.
//
// (v1: internal/config/storage.go StorageConfig.BlockCacheSize/
// MemTableSize/MaxKeySize/MaxValueSize/TombstoneTTL/
// VClockPruneThreshold helpers)
func (c StorageConfig) Pebble(dataDir string, nodeID node.NodeID) pebblestore.Options {
	return pebblestore.Options{
		DataDir:                 dataDir,
		NodeID:                  nodeID,
		SyncWrites:              c.SyncWrites,
		BlockCacheSize:          int64(c.CacheSizeMB) << 20,
		MemTableSize:            c.MemTableMB << 20,
		MaxOpenFiles:            c.MaxOpenFiles,
		MaxKeySize:              c.MaxKeySizeKB << 10,
		MaxValueSize:            c.MaxValueSizeMB << 20,
		MaxSiblings:             c.MaxSiblings,
		SiblingWarningThreshold: c.SiblingWarningThreshold,
		TombstoneGCEnabled:      c.TombstoneGCEnabled,
		TombstoneTTL:            time.Duration(c.TombstoneTTLDays) * 24 * time.Hour,
		TombstoneGCInterval:     c.TombstoneGCInterval,
		VClockPruneThreshold:    time.Duration(c.VClockPruneDays) * 24 * time.Hour,
		VClockMaxEntries:        c.VClockMaxEntries,
	}
}
