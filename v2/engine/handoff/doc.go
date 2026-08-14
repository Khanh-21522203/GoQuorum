// Package handoff buffers writes destined for a temporarily unreachable
// node (hinted handoff) and replays them once the node recovers.
//
// (v1: internal/cluster/hinted_handoff.go)
package handoff
