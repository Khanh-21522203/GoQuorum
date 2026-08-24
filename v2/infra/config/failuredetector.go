package config

import (
	"time"

	engineconfig "goquorum.io/v2/engine/config"
)

// FailureDetectorConfig controls heartbeat cadence, failure thresholds,
// and slow-node detection for the failure detector.
//
// v1 left this struct untagged; every field here carries an explicit tag.
//
// (v1: internal/config/failure_detector.go FailureDetectorConfig)
type FailureDetectorConfig struct {
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	HeartbeatTimeout  time.Duration `yaml:"heartbeat_timeout"`
	FailureThreshold  int           `yaml:"failure_threshold"`

	ReplicaRPCTimeout time.Duration `yaml:"replica_rpc_timeout"`

	SlowNodeLatencyThreshold time.Duration `yaml:"slow_node_latency_threshold"`
	SlowNodeTimeoutThreshold int           `yaml:"slow_node_timeout_threshold"`
}

// FailureDetector converts the loaded configuration into the engine/config
// value type engine/failuredetector consumes.
func (c *Config) FailureDetector() engineconfig.FailureDetectorConfig {
	return engineconfig.FailureDetectorConfig{
		HeartbeatInterval:        c.FailureDetectorConfig.HeartbeatInterval,
		HeartbeatTimeout:         c.FailureDetectorConfig.HeartbeatTimeout,
		FailureThreshold:         c.FailureDetectorConfig.FailureThreshold,
		ReplicaRPCTimeout:        c.FailureDetectorConfig.ReplicaRPCTimeout,
		SlowNodeLatencyThreshold: c.FailureDetectorConfig.SlowNodeLatencyThreshold,
		SlowNodeTimeoutThreshold: c.FailureDetectorConfig.SlowNodeTimeoutThreshold,
	}
}
