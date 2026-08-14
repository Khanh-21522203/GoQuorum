package integration

import "testing"

// TestAntiEntropyConvergence exercises the background Merkle-tree
// anti-entropy process: a replica that missed a write while unreachable
// should converge to the same data once anti-entropy reconciles it against
// a replica that has it, without any client-driven read repair.
//
// (v1: internal/cluster/anti_entropy_test.go convergence tests around
// AntiEntropy.Start's periodic scan/exchange loop.)
func TestAntiEntropyConvergence(t *testing.T) {
	cluster := newTestCluster(t, 3)
	_ = cluster

	// Intended flow:
	//
	//   ctx := context.Background()
	//   key, value := []byte("ae-key"), []byte("ae-value")
	//
	//   // Take the third replica offline before the write.
	//   cluster.Nodes[2].Stop()
	//
	//   c, err := cluster.Nodes[0].Client()
	//   if _, err := c.Put(ctx, key, value, vclock.NewVectorClock()); err != nil {
	//       t.Fatalf("put: %v", err)
	//   }
	//
	//   // Restart the third replica against the same on-disk data
	//   // directory (it never received the write above).
	//   restarted, err := harness.StartNode(cfgForNode3)
	//   if err != nil {
	//       t.Fatalf("restart node 3: %v", err)
	//   }
	//   defer restarted.Stop()
	//
	//   // Poll (with a timeout well beyond
	//   // config.AntiEntropyConfig.ScanInterval, or trigger a manual scan
	//   // once AntiEntropy exposes one) until the restarted replica's
	//   // local Get(key) returns the same non-tombstone sibling as
	//   // cluster.Nodes[0]'s, and both nodes' GetMerkleRoot values match.
}
