package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level YAML configuration for a GoQuorum node. It is
// infra's yaml-tagged loading representation; the conversion methods
// (Quorum, ReadRepair, AntiEntropy, FailureDetector, Timeout) map its
// tagged structs onto the untagged engine/config value types the domain
// core consumes.
//
// (v1: internal/config/config.go Config)
type Config struct {
	Node    NodeConfig    `yaml:"node"`
	Cluster ClusterConfig `yaml:"cluster"`
	Storage StorageConfig `yaml:"storage"`

	QuorumConfig          QuorumConfig          `yaml:"quorum"`
	ReadRepairConfig      ReadRepairConfig      `yaml:"read_repair"`
	AntiEntropyConfig     AntiEntropyConfig     `yaml:"anti_entropy"`
	FailureDetectorConfig FailureDetectorConfig `yaml:"failure_detector"`
	TimeoutConfig         TimeoutConfig         `yaml:"timeout"`

	Connection ConnectionConfig `yaml:"connection"`
	Server     ServerConfig     `yaml:"server"`
	Gossip     GossipConfig     `yaml:"gossip"`
}

// Load reads and parses the YAML configuration file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// applyDefaults fills in sensible default values for empty configuration fields.
func (c *Config) applyDefaults() {
	if c.Node.LogLevel == "" {
		c.Node.LogLevel = "info"
	}
	if c.Node.DataDir == "" {
		c.Node.DataDir = "./data"
	}

	if c.QuorumConfig.N == 0 {
		c.QuorumConfig.N = 3
		c.QuorumConfig.R = 2
		c.QuorumConfig.W = 2
	}

	if c.Cluster.NodeID == "" && c.Node.NodeID != "" {
		c.Cluster.NodeID = c.Node.NodeID
	}
	if c.Cluster.HeartbeatInterval == 0 {
		c.Cluster.HeartbeatInterval = time.Second
	}
	if c.Cluster.HeartbeatTimeout == 0 {
		c.Cluster.HeartbeatTimeout = 2 * time.Second
	}
	if c.Cluster.FailureThreshold == 0 {
		c.Cluster.FailureThreshold = 5
	}
	if c.Cluster.BootstrapTimeout == 0 {
		c.Cluster.BootstrapTimeout = 60 * time.Second
	}

	if c.Server.GRPCAddr == "" {
		c.Server.GRPCAddr = ":7070"
	}
	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = ":8080"
	}

	if c.Gossip.FanOut == 0 {
		c.Gossip.FanOut = 3
	}
	if c.Gossip.Interval == 0 {
		c.Gossip.Interval = time.Second
	}
}

// Validate checks the configuration for consistency and completeness.
func (c *Config) Validate() error {
	if c.Node.NodeID == "" {
		return fmt.Errorf("node.node_id is required")
	}
	if !c.Node.NodeID.Validate() {
		return fmt.Errorf("node.node_id %q is invalid", c.Node.NodeID)
	}
	if c.QuorumConfig.N < 1 {
		return fmt.Errorf("quorum.n must be >= 1")
	}
	if c.QuorumConfig.R < 1 || c.QuorumConfig.R > c.QuorumConfig.N {
		return fmt.Errorf("quorum.r (%d) must be between 1 and N (%d)", c.QuorumConfig.R, c.QuorumConfig.N)
	}
	if c.QuorumConfig.W < 1 || c.QuorumConfig.W > c.QuorumConfig.N {
		return fmt.Errorf("quorum.w (%d) must be between 1 and N (%d)", c.QuorumConfig.W, c.QuorumConfig.N)
	}
	return nil
}
