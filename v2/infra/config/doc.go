// Package config is the YAML-tagged loading representation of a GoQuorum
// node's configuration. It fixes a v1 bug where several nested structs
// (quorum, read-repair, anti-entropy, connection, and failure-detector
// config) had no yaml tags and so silently failed to deserialize; every
// field here carries an explicit tag. Load parses a YAML file into a
// *Config; the conversion methods (Quorum, ReadRepair, AntiEntropy,
// FailureDetector, Timeout) map its tagged structs onto the untagged
// engine/config value types the domain core consumes.
//
// (v1: internal/config/config.go)
package config
