package config

import "time"

// FailureDetectorConfig controls heartbeat cadence, failure thresholds, and
// slow-node detection for the failure detector.
//
// (v1: internal/config/failure_detector.go FailureDetectorConfig)
type FailureDetectorConfig struct {
	// Heartbeat configuration.
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	FailureThreshold  int // Missed heartbeats before a node is marked failed.

	// RPC timeouts.
	ReplicaRPCTimeout time.Duration

	// Slow node detection.
	SlowNodeLatencyThreshold time.Duration // p99 latency threshold.
	SlowNodeTimeoutThreshold int           // Timeout rate threshold, percent.
}

// DefaultFailureDetectorConfig returns the default failure detector
// configuration: 1s heartbeat interval, 2s timeout, 5 missed heartbeats to
// fail, 2s replica RPC timeout, 1s slow-node latency threshold, 10% slow
// timeout threshold.
func DefaultFailureDetectorConfig() FailureDetectorConfig {
	return FailureDetectorConfig{
		HeartbeatInterval:        1 * time.Second,
		HeartbeatTimeout:         2 * time.Second,
		FailureThreshold:         5,
		ReplicaRPCTimeout:        2 * time.Second,
		SlowNodeLatencyThreshold: 1 * time.Second,
		SlowNodeTimeoutThreshold: 10,
	}
}
