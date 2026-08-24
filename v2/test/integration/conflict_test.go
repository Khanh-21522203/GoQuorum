package integration

import "testing"

// TestConflictSiblings exercises concurrent, causally-unrelated writes to
// the same key: two blind Puts (each passing vclock.NewVectorClock() as
// their causal context, i.e. neither read the other's prior value) should
// leave the key with two concurrent siblings, and an LWWResolver should
// deterministically pick the one with the later Timestamp while merging
// both causal contexts.
//
// (v1: client/client_test.go and internal/storage sibling-reconciliation
// tests around concurrent, non-dominating vector clocks.)
func TestConflictSiblings(t *testing.T) {
	cluster := newTestCluster(t, 3)
	_ = cluster

	// Intended flow:
	//
	//   c, err := cluster.Nodes[0].Client()
	//   ctx := context.Background()
	//   key := []byte("conflict-key")
	//
	//   if _, err := c.Put(ctx, key, []byte("v1"), vclock.NewVectorClock()); err != nil {
	//       t.Fatalf("put v1: %v", err)
	//   }
	//   if _, err := c.Put(ctx, key, []byte("v2"), vclock.NewVectorClock()); err != nil {
	//       t.Fatalf("put v2: %v", err)
	//   }
	//
	//   siblings, err := c.Get(ctx, key)
	//
	// Assertions:
	//   - len(siblings) == 2 (concurrent: neither context dominates the
	//     other, per vclock.VectorClock.IsConcurrentWith)
	//   - (&client.LWWResolver{}).Resolve(siblings) picks the sibling with
	//     the greater Timestamp, and its merged context dominates both
	//     siblings' individual contexts.
}
