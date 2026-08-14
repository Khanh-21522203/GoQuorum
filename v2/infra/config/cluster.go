package config

import (
	"time"

	"goquorum.io/v2/contracts/node"
)

// MemberConfig defines one statically-configured cluster member.
//
// (v1: internal/config/cluster.go MemberConfig)
type MemberConfig struct {
	ID       node.NodeID `yaml:"id"`
	Addr     string      `yaml:"addr"`      // gRPC/replica <host>:<port>.
	HTTPAddr string      `yaml:"http_addr"` // Internal-RPC <host>:<port>.
}

// ClusterConfig defines static cluster membership and heartbeat tuning.
//
// (v1: internal/config/cluster.go ClusterConfig)
type ClusterConfig struct {
	NodeID     node.NodeID    `yaml:"node_id"`
	ListenAddr string         `yaml:"listen_addr"`
	Members    []MemberConfig `yaml:"members"`

	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	HeartbeatTimeout  time.Duration `yaml:"heartbeat_timeout"`
	FailureThreshold  int           `yaml:"failure_threshold"`

	BootstrapTimeout time.Duration `yaml:"bootstrap_timeout"`
}
