package config

import "time"

// TimeoutConfig bundles the per-request timeouts the coordinator applies to
// client calls, replica RPCs, and repair RPCs.
//
// (v1: internal/cluster/coordinator.go TimeoutConfig, inlined with these
// same default values in NewCoordinator rather than exposed as a
// standalone config type.)
type TimeoutConfig struct {
	ClientTimeout  time.Duration
	ReplicaTimeout time.Duration
	RepairTimeout  time.Duration
}

// DefaultTimeoutConfig returns the default timeout configuration: 5s client
// timeout, 2s replica timeout, 1s repair timeout.
func DefaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{
		ClientTimeout:  5 * time.Second,
		ReplicaTimeout: 2 * time.Second,
		RepairTimeout:  1 * time.Second,
	}
}
