package config

import (
	"time"

	engineconfig "goquorum.io/v2/engine/config"
)

// ReadRepairConfig controls read-repair behaviour: whether stale replicas
// are patched up synchronously or in the background after a quorum read.
//
// v1 left this struct untagged; every field here carries an explicit tag.
//
// (v1: internal/config/repair.go ReadRepairConfig)
type ReadRepairConfig struct {
	Enabled     bool          `yaml:"enabled"`
	Async       bool          `yaml:"async"`
	Timeout     time.Duration `yaml:"timeout"`
	Probability float64       `yaml:"probability"`
}

// ReadRepair converts the loaded configuration into the engine/config
// value type engine/readrepair consumes.
func (c *Config) ReadRepair() engineconfig.ReadRepairConfig {
	return engineconfig.ReadRepairConfig{
		Enabled:     c.ReadRepairConfig.Enabled,
		Async:       c.ReadRepairConfig.Async,
		Timeout:     c.ReadRepairConfig.Timeout,
		Probability: c.ReadRepairConfig.Probability,
	}
}

// AntiEntropyConfig controls the background Merkle-tree anti-entropy
// process that reconciles replicas out of band.
//
// v1 left this struct untagged; every field here carries an explicit tag.
//
// (v1: internal/config/repair.go AntiEntropyConfig)
type AntiEntropyConfig struct {
	Enabled         bool          `yaml:"enabled"`
	ScanInterval    time.Duration `yaml:"scan_interval"`
	ExchangeTimeout time.Duration `yaml:"exchange_timeout"`
	MaxBandwidth    int64         `yaml:"max_bandwidth"`
	Parallelism     int           `yaml:"parallelism"`
	MerkleDepth     int           `yaml:"merkle_depth"`
}

// AntiEntropy converts the loaded configuration into the engine/config
// value type engine/antientropy consumes.
func (c *Config) AntiEntropy() engineconfig.AntiEntropyConfig {
	return engineconfig.AntiEntropyConfig{
		Enabled:         c.AntiEntropyConfig.Enabled,
		ScanInterval:    c.AntiEntropyConfig.ScanInterval,
		ExchangeTimeout: c.AntiEntropyConfig.ExchangeTimeout,
		MaxBandwidth:    c.AntiEntropyConfig.MaxBandwidth,
		Parallelism:     c.AntiEntropyConfig.Parallelism,
		MerkleDepth:     c.AntiEntropyConfig.MerkleDepth,
	}
}
