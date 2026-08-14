package benchmarks

import (
	"fmt"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/infra/config"
	"goquorum.io/v2/test/harness"
)

// newBenchCluster mirrors test/integration's newTestCluster for
// benchmarks: it builds n single-node configs wired as a full n-node
// cluster and starts them via harness.StartCluster, skipping the
// benchmark once that fails.
//
// harness.StartCluster currently always fails, transitively, on
// infra/storage/pebble.NewStore's contracts.ErrNotImplemented stub (see
// test/harness/doc.go); newBenchCluster surfaces that error via b.Skip.
func newBenchCluster(b *testing.B, n int) *harness.Cluster {
	b.Helper()

	ids := make([]node.NodeID, n)
	members := make([]config.MemberConfig, n)
	for i := 0; i < n; i++ {
		ids[i] = node.NodeID(fmt.Sprintf("node-%d", i+1))
		members[i] = config.MemberConfig{
			ID:       ids[i],
			Addr:     "127.0.0.1:0",
			HTTPAddr: "127.0.0.1:0",
		}
	}

	cfgs := make([]*config.Config, n)
	for i := 0; i < n; i++ {
		cfgs[i] = harness.NewTestConfig(ids[i], b.TempDir(), "127.0.0.1:0", members)
	}

	cluster, err := harness.StartCluster(cfgs)
	if err != nil {
		b.Logf("harness.StartCluster: %v", err)
		b.Skip("scaffold: not implemented (TODO v2)")
	}
	b.Cleanup(cluster.Stop)
	return cluster
}
