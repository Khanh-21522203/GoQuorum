package config

import (
	"errors"
	"fmt"
	"time"
)

// ReadRepairConfig configures read repair behavior (Section 3.4)
type ReadRepairConfig struct {
	Enabled     bool          // Enable read repair (default: true)
	Async       bool          // Non-blocking repair (default: true)
	Timeout     time.Duration // Repair RPC timeout (default: 1s)
	Probability float64       // Trigger probability (default: 1.0)
}

// DefaultReadRepairConfig returns default read repair configuration
func DefaultReadRepairConfig() ReadRepairConfig {
	return ReadRepairConfig{
		Enabled:     true,
		Async:       true,
		Timeout:     1 * time.Second,
		Probability: 1.0, // Always trigger
	}
}

func (c ReadRepairConfig) Validate() error {
	if c.Timeout <= 0 {
		return errors.New("timeout must be > 0")
	}
	if c.Probability < 0 || c.Probability > 1 {
		return errors.New("probability must be between 0 and 1")
	}
	return nil
}

// Status returns human-readable status
func (c ReadRepairConfig) Status() string {
	if !c.Enabled {
		return "disabled"
	}
	mode := "sync"
	if c.Async {
		mode = "async"
	}
	return fmt.Sprintf("enabled (%s, probability=%.0f%%)", mode, c.Probability*100)
}

// AntiEntropyConfig configures anti-entropy synchronization (Section 4.7)
type AntiEntropyConfig struct {
	Enabled         bool          // Enable anti-entropy (default: true)
	ScanInterval    time.Duration // Full scan interval (default: 1h)
	ExchangeTimeout time.Duration // Single exchange timeout (default: 30s)
	MaxBandwidth    int64         // Max bytes/sec (default: 10MB/s)
	Parallelism     int           // Concurrent exchanges (default: 1)
	MerkleDepth     int           // Merkle tree depth (default: 10)
}

// DefaultAntiEntropyConfig returns default anti-entropy configuration
func DefaultAntiEntropyConfig() AntiEntropyConfig {
	return AntiEntropyConfig{
		Enabled:         true,
		ScanInterval:    1 * time.Hour,
		ExchangeTimeout: 30 * time.Second,
		MaxBandwidth:    10 * 1024 * 1024, // 10 MB/s
		Parallelism:     1,
		MerkleDepth:     10, // 1024 buckets
	}
}

func (c AntiEntropyConfig) Validate() error {
	if c.ScanInterval <= 0 {
		return errors.New("scan_interval must be > 0")
	}
	if c.ExchangeTimeout <= 0 {
		return errors.New("exchange_timeout must be > 0")
	}
	if c.MaxBandwidth <= 0 {
		return errors.New("max_bandwidth must be > 0")
	}
	if c.Parallelism <= 0 {
		return errors.New("parallelism must be > 0")
	}
	if c.MerkleDepth < 1 || c.MerkleDepth > 20 {
		return errors.New("merkle_depth must be between 1 and 20")
	}
	return nil
}

// NumBuckets returns number of leaf buckets in Merkle tree
func (c AntiEntropyConfig) NumBuckets() int {
	return 1 << c.MerkleDepth // 2^depth
}

// Status returns human-readable status
func (c AntiEntropyConfig) Status() string {
	if !c.Enabled {
		return "disabled"
	}
	return fmt.Sprintf("enabled (interval=%s, buckets=%d)", c.ScanInterval, c.NumBuckets())
}

// RepairConfig configures repair queue and workers (Section 5.2)
type RepairConfig struct {
	QueueSize   int           // Repair queue capacity (default: 10000)
	Workers     int           // Repair worker count (default: 4)
	MaxAttempts int           // Max retry attempts (default: 3)
	RetryDelay  time.Duration // Base retry delay (default: 1s)
}

// DefaultRepairConfig returns default repair configuration
func DefaultRepairConfig() RepairConfig {
	return RepairConfig{
		QueueSize:   10000,
		Workers:     4,
		MaxAttempts: 3,
		RetryDelay:  1 * time.Second,
	}
}

func (c RepairConfig) Validate() error {
	if c.QueueSize <= 0 {
		return errors.New("queue_size must be > 0")
	}
	if c.Workers <= 0 {
		return errors.New("workers must be > 0")
	}
	if c.MaxAttempts <= 0 {
		return errors.New("max_attempts must be > 0")
	}
	if c.RetryDelay <= 0 {
		return errors.New("retry_delay must be > 0")
	}
	return nil
}
