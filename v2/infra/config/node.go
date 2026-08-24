package config

import "goquorum.io/v2/contracts/node"

// NodeConfig defines local node settings.
//
// (v1: internal/config/config.go NodeConfig)
type NodeConfig struct {
	NodeID   node.NodeID `yaml:"node_id"`
	DataDir  string      `yaml:"data_dir"`
	LogLevel string      `yaml:"log_level"`

	// ReactorCPUCore, if set, pins the reactor goroutine (see
	// server/app.Server.Run) to this CPU core via infra/affinity.
	// Unset (nil) means no pinning, the default.
	ReactorCPUCore *int `yaml:"reactor_cpu_core"`
}
