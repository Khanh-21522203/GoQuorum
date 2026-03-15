package config

import (
	"GoQuorum/internal/common"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// GossipConfig holds tuning parameters for the gossip membership protocol.
// It mirrors cluster.GossipConfig but lives in the config package to avoid
// a circular import.
type GossipConfig struct {
	Enabled  bool          `yaml:"enabled"`
	FanOut   int           `yaml:"fan_out"`  // peers to gossip to per round (default 3)
	Interval time.Duration `yaml:"interval"` // gossip period (default 1s)
}

// Config is the top-level configuration
type Config struct {
	Node        NodeConfig        `yaml:"node"`
	Cluster     ClusterConfig     `yaml:"cluster"`
	Storage     StorageConfig     `yaml:"storage"`
	Quorum      QuorumConfig      `yaml:"quorum"`
	ReadRepair  ReadRepairConfig  `yaml:"read_repair"`
	AntiEntropy AntiEntropyConfig `yaml:"anti_entropy"`
	Connection  ConnectionConfig  `yaml:"connection"`
	Server      ServerConfig      `yaml:"server"`
	Gossip      GossipConfig      `yaml:"gossip"`
}

// NodeConfig defines local node settings
type NodeConfig struct {
	NodeID   common.NodeID `yaml:"node_id"`
	DataDir  string        `yaml:"data_dir"`
	LogLevel string        `yaml:"log_level"`
}

// RateLimitConfig controls token-bucket rate limiting on the gRPC server.
type RateLimitConfig struct {
	GlobalRPS   float64 `yaml:"global_rps"`   // 0 = disabled
	PerIPRPS    float64 `yaml:"per_ip_rps"`   // 0 = no per-IP limit
	BurstFactor float64 `yaml:"burst_factor"` // default 1.0
}

// ServerConfig defines gRPC/HTTP server settings
type ServerConfig struct {
	GRPCAddr  string          `yaml:"grpc_addr"`
	HTTPAddr  string          `yaml:"http_addr"`
	TLS       TLSConfig       `yaml:"tls"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// LoadConfig loads configuration from YAML file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Apply defaults
	cfg.applyDefaults()

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// applyDefaults applies default values for missing configs
func (c *Config) applyDefaults() {
	// Node defaults
	if c.Node.LogLevel == "" {
		c.Node.LogLevel = "info"
	}
	if c.Node.DataDir == "" {
		c.Node.DataDir = "./data"
	}

	// Quorum defaults
	if c.Quorum.N == 0 {
		c.Quorum = DefaultQuorumConfig()
	}

	// Read repair defaults
	if c.ReadRepair.Timeout == 0 {
		c.ReadRepair = DefaultReadRepairConfig()
	}

	// Anti-entropy defaults
	if c.AntiEntropy.ScanInterval == 0 {
		c.AntiEntropy = DefaultAntiEntropyConfig()
	}

	// Connection defaults
	if c.Connection.PoolSize == 0 {
		c.Connection = DefaultConnectionConfig()
	}

	// Storage defaults
	if c.Storage.CacheSizeMB == 0 {
		c.Storage = DefaultStorageConfig()
	}

	// Cluster defaults
	if c.Cluster.NodeID == "" && c.Node.NodeID != "" {
		c.Cluster.NodeID = c.Node.NodeID
	}
	if c.Cluster.HeartbeatInterval == 0 {
		c.Cluster.HeartbeatInterval = 1 * time.Second
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

	// Server defaults
	if c.Server.GRPCAddr == "" {
		c.Server.GRPCAddr = ":7070"
	}
	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = ":8080"
	}

	// Gossip defaults
	if c.Gossip.FanOut == 0 {
		c.Gossip.FanOut = 3
	}
	if c.Gossip.Interval == 0 {
		c.Gossip.Interval = 1 * time.Second
	}
}

// Validate validates the entire configuration
func (c *Config) Validate() error {
	// Validate node
	if c.Node.NodeID == "" {
		return fmt.Errorf("node.node_id is required")
	}
	if err := ValidateNodeID(c.Node.NodeID); err != nil {
		return fmt.Errorf("node.node_id: %w", err)
	}

	// Validate cluster
	if err := c.Cluster.Validate(); err != nil {
		return fmt.Errorf("cluster: %w", err)
	}

	// Validate quorum
	if err := c.Quorum.Validate(); err != nil {
		return fmt.Errorf("quorum: %w", err)
	}

	// Check quorum compatibility with cluster size
	if c.Quorum.N > len(c.Cluster.Members) {
		return fmt.Errorf("quorum.N (%d) cannot exceed cluster size (%d)",
			c.Quorum.N, len(c.Cluster.Members))
	}

	// Validate read repair
	if err := c.ReadRepair.Validate(); err != nil {
		return fmt.Errorf("read_repair: %w", err)
	}

	// Validate anti-entropy
	if err := c.AntiEntropy.Validate(); err != nil {
		return fmt.Errorf("anti_entropy: %w", err)
	}

	// Validate connection
	if err := c.Connection.Validate(); err != nil {
		return fmt.Errorf("connection: %w", err)
	}

	// Validate storage
	if err := c.Storage.Validate(); err != nil {
		return fmt.Errorf("storage: %w", err)
	}

	return nil
}

// PrintSummary prints configuration summary
func (c *Config) PrintSummary() {
	fmt.Println("=== GoQuorum Configuration ===")
	fmt.Printf("Node ID: %s\n", c.Node.NodeID)
	fmt.Printf("Data Dir: %s\n", c.Node.DataDir)
	fmt.Printf("Listen Addr: %s\n", c.Cluster.ListenAddr)
	fmt.Printf("Cluster Size: %d nodes\n", len(c.Cluster.Members))
	fmt.Printf("Quorum: %s\n", c.Quorum.String())
	fmt.Printf("Read Repair: %s\n", c.ReadRepair.Status())
	fmt.Printf("Anti-Entropy: %s\n", c.AntiEntropy.Status())
	fmt.Println("==============================")
}
