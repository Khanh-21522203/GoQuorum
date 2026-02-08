package config

import (
	"errors"
	"fmt"
)

// QuorumConfig defines N/R/W quorum parameters
type QuorumConfig struct {
	N int // Replication factor (total replicas)
	R int // Read quorum (min successful reads)
	W int // Write quorum (min successful writes)
}

// DefaultQuorumConfig returns safe default configuration
func DefaultQuorumConfig() QuorumConfig {
	return QuorumConfig{
		N: 3, // 3 replicas
		R: 2, // Read from 2
		W: 2, // Write to 2
	}
}

// Validate ensures quorum properties are valid
func (q QuorumConfig) Validate() error {
	// Basic bounds
	if q.N < 1 {
		return errors.New("N must be >= 1")
	}
	if q.R < 1 || q.R > q.N {
		return fmt.Errorf("R must be in [1, N], got R=%d, N=%d", q.R, q.N)
	}
	if q.W < 1 || q.W > q.N {
		return fmt.Errorf("W must be in [1, N], got W=%d, N=%d", q.W, q.N)
	}

	return nil
}

// ConsistencyLevel returns the consistency level provided by this config
func (q QuorumConfig) ConsistencyLevel() string {
	if q.R+q.W > q.N {
		return "strong" // Read-write overlap guaranteed
	}
	return "eventual" // No guaranteed overlap
}

// IsStrongConsistency checks if configuration guarantees strong consistency
func (q QuorumConfig) IsStrongConsistency() bool {
	return q.R+q.W > q.N
}

// ToleratedFailures returns max failures system can tolerate
func (q QuorumConfig) ToleratedFailures() int {
	// System can tolerate N - max(R, W) failures
	maxQuorum := q.R
	if q.W > maxQuorum {
		maxQuorum = q.W
	}
	return q.N - maxQuorum
}

// String returns human-readable description
func (q QuorumConfig) String() string {
	return fmt.Sprintf("N=%d/R=%d/W=%d (consistency=%s, tolerated_failures=%d)",
		q.N, q.R, q.W, q.ConsistencyLevel(), q.ToleratedFailures())
}

// Common quorum configurations

// StrongConsistencyConfig returns N=3, R=2, W=2 (strong consistency)
func StrongConsistencyConfig() QuorumConfig {
	return QuorumConfig{N: 3, R: 2, W: 2}
}

// EventualConsistencyConfig returns N=3, R=1, W=1 (eventual consistency)
func EventualConsistencyConfig() QuorumConfig {
	return QuorumConfig{N: 3, R: 1, W: 1}
}

// ReadOptimizedConfig returns N=3, R=1, W=3 (fast reads, slow writes)
func ReadOptimizedConfig() QuorumConfig {
	return QuorumConfig{N: 3, R: 1, W: 3}
}

// WriteOptimizedConfig returns N=3, R=3, W=1 (fast writes, slow reads)
func WriteOptimizedConfig() QuorumConfig {
	return QuorumConfig{N: 3, R: 3, W: 1}
}

// HighAvailabilityConfig returns N=5, R=2, W=2 (tolerates 3 failures)
func HighAvailabilityConfig() QuorumConfig {
	return QuorumConfig{N: 5, R: 2, W: 2}
}
