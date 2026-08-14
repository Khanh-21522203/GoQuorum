package config

import "time"

// GossipConfig holds tuning parameters for the gossip membership protocol.
//
// (v1: internal/config/config.go GossipConfig)
type GossipConfig struct {
	Enabled  bool          `yaml:"enabled"`
	FanOut   int           `yaml:"fan_out"`  // Peers to gossip to per round. Default: 3.
	Interval time.Duration `yaml:"interval"` // Gossip period. Default: 1s.
}
