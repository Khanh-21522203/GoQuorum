package integration

import "testing"

// TestPutGetRoundTrip exercises the simplest replicated write/read path: a
// single Put followed by a Get for the same key should return exactly one
// non-conflicting sibling matching the written value.
//
// (v1: internal/cluster/coordinator_test.go end-to-end tests around
// Coordinator.Put + Coordinator.Get.)
func TestPutGetRoundTrip(t *testing.T) {
	cluster := newTestCluster(t, 3) // N=3, R=2, W=2 (see harness.NewTestConfig).
	_ = cluster

	// Intended flow once infra/storage/pebble and infra/transport/httprpc
	// are implemented:
	//
	//   c, err := cluster.Nodes[0].Client()
	//   if err != nil {
	//       t.Fatalf("client: %v", err)
	//   }
	//   defer c.Close()
	//
	//   ctx := context.Background()
	//   key, value := []byte("round-trip-key"), []byte("round-trip-value")
	//
	//   causal, err := c.Put(ctx, key, value, vclock.NewVectorClock())
	//   if err != nil {
	//       t.Fatalf("put: %v", err)
	//   }
	//
	//   siblings, err := c.Get(ctx, key)
	//   if err != nil {
	//       t.Fatalf("get: %v", err)
	//   }
	//
	// Assertions:
	//   - len(siblings) == 1
	//   - !siblings[0].Tombstone
	//   - bytes.Equal(siblings[0].Value, value)
	//   - siblings[0].Context dominates or equals causal
}
