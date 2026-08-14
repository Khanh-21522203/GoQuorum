// Package antientropy reconciles replicas out of band using Merkle trees:
// each node maintains a tree over its keyspace, compares roots with peers,
// and exchanges only the diverging buckets.
//
// (v1: internal/cluster/anti_entropy.go, merkle_tree.go)
package antientropy
