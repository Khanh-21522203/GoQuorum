// Package config holds engine-local configuration value types: plain
// structs with no yaml tags. Loading configuration from disk (yaml
// unmarshalling) is an infra concern; engine only needs the typed values.
//
// (v1: internal/config/quorum.go, repair.go, failure_detector.go)
package config
