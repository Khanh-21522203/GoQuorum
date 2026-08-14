// Package readrepair patches stale replicas discovered during a quorum
// read by re-writing the merged, dominant sibling set back to them.
//
// (v1: internal/cluster/read_repair.go)
package readrepair
