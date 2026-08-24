package config

import "time"

// ReadRepairConfig controls read-repair behaviour: whether stale replicas
// are patched up synchronously or in the background after a quorum read.
//
// (v1: internal/config/repair.go ReadRepairConfig)
type ReadRepairConfig struct {
	Enabled     bool          // Enable read repair.
	Async       bool          // Non-blocking repair.
	Timeout     time.Duration // Repair RPC timeout.
	Probability float64       // Trigger probability, in [0, 1].
}

// DefaultReadRepairConfig returns the default read-repair configuration:
// enabled, asynchronous, 1s timeout, always triggered.
func DefaultReadRepairConfig() ReadRepairConfig {
	return ReadRepairConfig{
		Enabled:     true,
		Async:       true,
		Timeout:     1 * time.Second,
		Probability: 1.0,
	}
}

// AntiEntropyConfig controls the background Merkle-tree anti-entropy
// process that reconciles replicas out of band.
//
// (v1: internal/config/repair.go AntiEntropyConfig)
type AntiEntropyConfig struct {
	Enabled         bool          // Enable anti-entropy.
	ScanInterval    time.Duration // Full scan interval.
	ExchangeTimeout time.Duration // Single exchange timeout.
	MaxBandwidth    int64         // Max bytes/sec.
	Parallelism     int           // Concurrent exchanges.
	MerkleDepth     int           // Merkle tree depth (buckets = 2^depth).
}

// DefaultAntiEntropyConfig returns the default anti-entropy configuration:
// enabled, hourly scan, 30s exchange timeout, 10MB/s, depth 10 (1024
// buckets).
func DefaultAntiEntropyConfig() AntiEntropyConfig {
	return AntiEntropyConfig{
		Enabled:         true,
		ScanInterval:    1 * time.Hour,
		ExchangeTimeout: 30 * time.Second,
		MaxBandwidth:    10 * 1024 * 1024,
		Parallelism:     1,
		MerkleDepth:     10,
	}
}
