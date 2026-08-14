package config

// QuorumConfig controls the replication factor and read/write quorum sizes
// for a single keyspace.
//
// (v1: internal/config/quorum.go QuorumConfig)
type QuorumConfig struct {
	N            int  // Replication factor (total replicas).
	R            int  // Read quorum (min successful reads).
	W            int  // Write quorum (min successful writes).
	SloppyQuorum bool // If true, use overflow nodes when strict quorum is unachievable.
}

// DefaultQuorumConfig returns the default quorum configuration: N=3, R=2,
// W=2, strict quorum.
func DefaultQuorumConfig() QuorumConfig {
	return QuorumConfig{N: 3, R: 2, W: 2, SloppyQuorum: false}
}
