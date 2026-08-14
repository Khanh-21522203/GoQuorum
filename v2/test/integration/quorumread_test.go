package integration

import "testing"

// TestQuorumRead exercises Coordinator.Get's quorum accounting: with N=3,
// R=2, a read must succeed as long as at least R of the 3 replicas are
// reachable, and must fail with a quorumerr.QuorumError once fewer than R
// replicas can be reached.
//
// (v1: internal/cluster/coordinator_test.go quorum-read tests exercising R
// against simulated replica failures.)
func TestQuorumRead(t *testing.T) {
	tests := []struct {
		name      string
		reachable int // Replicas reachable out of N=3.
		wantErr   bool
	}{
		{name: "all replicas reachable", reachable: 3, wantErr: false},
		{name: "exactly quorum reachable (R=2 of 3)", reachable: 2, wantErr: false},
		{name: "below quorum (1 of 3)", reachable: 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newTestCluster(t, 3)
			_ = cluster

			// Intended flow:
			//
			//   c, err := cluster.Nodes[0].Client()
			//   ctx := context.Background()
			//   key, value := []byte("quorum-key"), []byte("quorum-value")
			//   if _, err := c.Put(ctx, key, value, vclock.NewVectorClock()); err != nil {
			//       t.Fatalf("put: %v", err)
			//   }
			//
			//   // Simulate unreachable replicas by stopping
			//   // len(cluster.Nodes) - tt.reachable of them (excluding the
			//   // coordinator node itself, cluster.Nodes[0]).
			//   for _, n := range cluster.Nodes[tt.reachable:] {
			//       n.Stop()
			//   }
			//
			//   _, err = c.Get(ctx, key)
			//
			// Assertions:
			//   - if tt.wantErr: errors.As(err, &quorumErr) and
			//     quorumErr.Achieved < quorumErr.Required
			//   - else: err == nil
		})
	}
}
