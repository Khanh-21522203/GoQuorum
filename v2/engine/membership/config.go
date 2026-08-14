package membership

import (
	"time"

	"goquorum.io/v2/contracts/node"
)

// MemberConfig describes one statically-configured cluster member.
//
// (v1: internal/config/cluster.go MemberConfig)
type MemberConfig struct {
	ID       node.NodeID
	Addr     string // gRPC/replica address.
	HTTPAddr string // Internal-RPC address.
}

// Config is the engine-local membership configuration. It mirrors v1's
// config.ClusterConfig, but lives in engine (no yaml tags: loading from
// disk is an infra concern).
//
// (v1: internal/config/cluster.go ClusterConfig)
type Config struct {
	NodeID            node.NodeID
	ListenAddr        string
	Members           []MemberConfig
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	FailureThreshold  int
	BootstrapTimeout  time.Duration
}
