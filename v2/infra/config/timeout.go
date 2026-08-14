package config

import (
	"time"

	engineconfig "goquorum.io/v2/engine/config"
)

// TimeoutConfig bundles the per-request timeouts the coordinator applies
// to client calls, replica RPCs, and repair RPCs.
//
// v1 inlined these same default values directly in NewCoordinator rather
// than exposing them as a standalone, loadable config type; v2 promotes it
// to a proper YAML section so it can be tuned without a rebuild.
//
// (v1: internal/cluster/coordinator.go TimeoutConfig)
type TimeoutConfig struct {
	ClientTimeout  time.Duration `yaml:"client_timeout"`
	ReplicaTimeout time.Duration `yaml:"replica_timeout"`
	RepairTimeout  time.Duration `yaml:"repair_timeout"`
}

// Timeout converts the loaded configuration into the engine/config value
// type engine/coordinator consumes.
func (c *Config) Timeout() engineconfig.TimeoutConfig {
	return engineconfig.TimeoutConfig{
		ClientTimeout:  c.TimeoutConfig.ClientTimeout,
		ReplicaTimeout: c.TimeoutConfig.ReplicaTimeout,
		RepairTimeout:  c.TimeoutConfig.RepairTimeout,
	}
}
