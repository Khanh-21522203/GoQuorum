package config

import (
	"errors"
	"time"
)

// FailureDetectorConfig configures failure detection (Section 9)
type FailureDetectorConfig struct {
	// Heartbeat configuration (Section 9.1)
	HeartbeatInterval time.Duration // Default: 1s
	HeartbeatTimeout  time.Duration // Default: 2s
	FailureThreshold  int           // Default: 5 missed heartbeats

	// RPC timeouts (Section 6.3)
	ReplicaRPCTimeout time.Duration // Default: 2s

	// Slow node detection (Section 6.2)
	SlowNodeLatencyThreshold time.Duration // Default: 1s (p99)
	SlowNodeTimeoutThreshold int           // Default: 10% timeout rate
}

// DefaultFailureDetectorConfig returns default configuration (Section 9.1)
func DefaultFailureDetectorConfig() FailureDetectorConfig {
	return FailureDetectorConfig{
		HeartbeatInterval:        1 * time.Second,
		HeartbeatTimeout:         2 * time.Second,
		FailureThreshold:         5,
		ReplicaRPCTimeout:        2 * time.Second,
		SlowNodeLatencyThreshold: 1 * time.Second,
		SlowNodeTimeoutThreshold: 10, // 10%
	}
}

// AggressiveFailureDetectorConfig for fast detection (Section 9.1)
func AggressiveFailureDetectorConfig() FailureDetectorConfig {
	return FailureDetectorConfig{
		HeartbeatInterval:        500 * time.Millisecond,
		HeartbeatTimeout:         1 * time.Second,
		FailureThreshold:         3,
		ReplicaRPCTimeout:        1 * time.Second,
		SlowNodeLatencyThreshold: 500 * time.Millisecond,
		SlowNodeTimeoutThreshold: 5,
	}
}

// ConservativeFailureDetectorConfig for stable detection (Section 9.1)
func ConservativeFailureDetectorConfig() FailureDetectorConfig {
	return FailureDetectorConfig{
		HeartbeatInterval:        2 * time.Second,
		HeartbeatTimeout:         5 * time.Second,
		FailureThreshold:         10,
		ReplicaRPCTimeout:        5 * time.Second,
		SlowNodeLatencyThreshold: 2 * time.Second,
		SlowNodeTimeoutThreshold: 20,
	}
}

// DetectionTime returns expected failure detection time (Section 9.1)
func (c FailureDetectorConfig) DetectionTime() time.Duration {
	// Detection time = FailureThreshold * (HeartbeatInterval + HeartbeatTimeout)
	return time.Duration(c.FailureThreshold) * (c.HeartbeatInterval + c.HeartbeatTimeout)
}

func (c FailureDetectorConfig) Validate() error {
	if c.HeartbeatInterval <= 0 {
		return errors.New("heartbeat_interval must be > 0")
	}
	if c.HeartbeatTimeout <= 0 {
		return errors.New("heartbeat_timeout must be > 0")
	}
	if c.FailureThreshold <= 0 {
		return errors.New("failure_threshold must be > 0")
	}
	if c.ReplicaRPCTimeout <= 0 {
		return errors.New("replica_rpc_timeout must be > 0")
	}
	return nil
}
