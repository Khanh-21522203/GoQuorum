package config

import (
	"errors"
	"time"
)

// StorageConfig defines storage settings for YAML configuration
// This is a lightweight config that gets converted to storage.StorageOptions at runtime
type StorageConfig struct {
	// Basic settings
	SyncWrites   bool `yaml:"sync_writes"`   // Sync writes to disk (default: true)
	CacheSizeMB  int  `yaml:"cache_size_mb"` // Block cache size in MB (default: 256)
	MemTableMB   int  `yaml:"memtable_mb"`   // MemTable size in MB (default: 64)
	MaxOpenFiles int  `yaml:"max_open_files"` // Max open file handles (default: 1000)

	// Data limits
	MaxKeySizeKB   int `yaml:"max_key_size_kb"`   // Max key size in KB (default: 64)
	MaxValueSizeMB int `yaml:"max_value_size_mb"` // Max value size in MB (default: 1)
	MaxSiblings    int `yaml:"max_siblings"`      // Max concurrent versions (default: 100)

	// Tombstone GC
	TombstoneGCEnabled  bool          `yaml:"tombstone_gc_enabled"`  // Enable tombstone GC (default: true)
	TombstoneTTLDays    int           `yaml:"tombstone_ttl_days"`    // Tombstone TTL in days (default: 7)
	TombstoneGCInterval time.Duration `yaml:"tombstone_gc_interval"` // GC interval (default: 1h)

	// Vector clock pruning
	VClockPruneDays  int `yaml:"vclock_prune_days"`  // VClock prune threshold in days (default: 7)
	VClockMaxEntries int `yaml:"vclock_max_entries"` // Max VClock entries (default: 50)
}

// DefaultStorageConfig returns default storage configuration
func DefaultStorageConfig() StorageConfig {
	return StorageConfig{
		SyncWrites:   true,
		CacheSizeMB:  256,
		MemTableMB:   64,
		MaxOpenFiles: 1000,

		MaxKeySizeKB:   64,
		MaxValueSizeMB: 1,
		MaxSiblings:    100,

		TombstoneGCEnabled:  true,
		TombstoneTTLDays:    7,
		TombstoneGCInterval: 1 * time.Hour,

		VClockPruneDays:  7,
		VClockMaxEntries: 50,
	}
}

// Validate validates storage configuration
func (c StorageConfig) Validate() error {
	if c.CacheSizeMB <= 0 {
		return errors.New("cache_size_mb must be > 0")
	}
	if c.MemTableMB <= 0 {
		return errors.New("memtable_mb must be > 0")
	}
	if c.MaxOpenFiles <= 0 {
		return errors.New("max_open_files must be > 0")
	}
	if c.MaxKeySizeKB <= 0 || c.MaxKeySizeKB > 64 {
		return errors.New("max_key_size_kb must be in (0, 64]")
	}
	if c.MaxValueSizeMB <= 0 || c.MaxValueSizeMB > 10 {
		return errors.New("max_value_size_mb must be in (0, 10]")
	}
	if c.MaxSiblings <= 0 {
		return errors.New("max_siblings must be > 0")
	}
	if c.TombstoneTTLDays < 0 {
		return errors.New("tombstone_ttl_days must be >= 0")
	}
	if c.VClockPruneDays < 0 {
		return errors.New("vclock_prune_days must be >= 0")
	}
	if c.VClockMaxEntries <= 0 {
		return errors.New("vclock_max_entries must be > 0")
	}
	return nil
}

// BlockCacheSize returns block cache size in bytes
func (c StorageConfig) BlockCacheSize() int64 {
	return int64(c.CacheSizeMB) << 20
}

// MemTableSize returns memtable size in bytes
func (c StorageConfig) MemTableSize() int {
	return c.MemTableMB << 20
}

// MaxKeySize returns max key size in bytes
func (c StorageConfig) MaxKeySize() int {
	return c.MaxKeySizeKB << 10
}

// MaxValueSize returns max value size in bytes
func (c StorageConfig) MaxValueSize() int {
	return c.MaxValueSizeMB << 20
}

// TombstoneTTL returns tombstone TTL as duration
func (c StorageConfig) TombstoneTTL() time.Duration {
	return time.Duration(c.TombstoneTTLDays) * 24 * time.Hour
}

// VClockPruneThreshold returns vclock prune threshold as duration
func (c StorageConfig) VClockPruneThreshold() time.Duration {
	return time.Duration(c.VClockPruneDays) * 24 * time.Hour
}
