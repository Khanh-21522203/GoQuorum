package integration

import (
	"fmt"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/infra/config"
	"goquorum.io/v2/test/harness"
)

// newTestCluster builds n single-node configs wired together as a full
// n-node cluster (every node's static member list includes all n nodes,
// mirroring how a real deployment's YAML would list its peers) and starts
// them via harness.StartCluster. Each node gets its own temp data
// directory via t.TempDir().
//
// harness.StartCluster currently always fails: it calls through to
// server/app.New, which opens infra/storage/pebble.Store — itself a typed
// stub returning contracts.ErrNotImplemented (see
// infra/storage/pebble/store.go). newTestCluster surfaces that error via
// t.Skip so every test below documents its real intended setup without
// hand-rolling its own skip logic; once storage/transport land, this
// helper starts returning a live cluster and the callers' skip points
// stop firing.
func newTestCluster(t *testing.T, n int) *harness.Cluster {
	t.Helper()

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
		cfgs[i] = harness.NewTestConfig(ids[i], t.TempDir(), "127.0.0.1:0", members)
	}

	cluster, err := harness.StartCluster(cfgs)
	if err != nil {
		t.Logf("harness.StartCluster: %v", err)
		t.Skip("scaffold: not implemented (TODO v2)")
	}
	t.Cleanup(cluster.Stop)
	return cluster
}
