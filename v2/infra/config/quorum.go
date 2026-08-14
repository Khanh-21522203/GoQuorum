package config

import engineconfig "goquorum.io/v2/engine/config"

// QuorumConfig defines N/R/W quorum parameters.
//
// v1 left this struct untagged, so it silently failed to deserialize from
// YAML; every field here carries an explicit tag (see CONVENTIONS.md).
//
// (v1: internal/config/quorum.go QuorumConfig)
type QuorumConfig struct {
	N            int  `yaml:"n"`
	R            int  `yaml:"r"`
	W            int  `yaml:"w"`
	SloppyQuorum bool `yaml:"sloppy_quorum"`
}

// Quorum converts the loaded configuration into the engine/config value
// type engine/coordinator consumes.
func (c *Config) Quorum() engineconfig.QuorumConfig {
	return engineconfig.QuorumConfig{
		N:            c.QuorumConfig.N,
		R:            c.QuorumConfig.R,
		W:            c.QuorumConfig.W,
		SloppyQuorum: c.QuorumConfig.SloppyQuorum,
	}
}
