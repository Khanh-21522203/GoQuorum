package config

import "goquorum.io/v2/contracts"

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
//
// TODO(v2): import gopkg.in/yaml.v3; read the file, unmarshal into Config,
// apply defaults for zero-valued sections (see v1's applyDefaults), and
// validate every section (v1: internal/config/config.go LoadConfig).
func Load(path string) (*Config, error) {
	return nil, contracts.ErrNotImplemented
}
