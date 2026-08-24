package harness

import (
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/infra/config"
)

// NewTestConfig returns a minimal config.Config for a single node: id,
// storing data under dataDir, and serving the client-facing HTTP API on
// httpAddr. members should list every node in the test cluster (including
// id itself) so the hash ring is populated the same way server/app.New
// does in production (see server/app/server.go New's step 3); pass nil
// for a single isolated node. The quorum defaults to N=3/R=2/W=2,
// matching engine/config.DefaultQuorumConfig's values.
func NewTestConfig(id node.NodeID, dataDir, httpAddr string, members []config.MemberConfig) *config.Config {
	return &config.Config{
		Node: config.NodeConfig{
			NodeID:  id,
			DataDir: dataDir,
		},
		Cluster: config.ClusterConfig{
			NodeID:     id,
			ListenAddr: httpAddr,
			Members:    members,
		},
		Server: config.ServerConfig{
			HTTPAddr: httpAddr,
		},
		QuorumConfig: config.QuorumConfig{N: 3, R: 2, W: 2},
	}
}
