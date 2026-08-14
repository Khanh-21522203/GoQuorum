package benchmarks

import "testing"

// BenchmarkMixed measures an 80% Get / 20% Put workload against a 3-node
// cluster (N=3), mirroring the v1 live-cluster tool's workloadMixed
// (GoQuorum/benchmarks/main.go).
func BenchmarkMixed(b *testing.B) {
	cluster := newBenchCluster(b, 3)
	_ = cluster

	// Intended flow:
	//
	//   c, err := cluster.Nodes[0].Client()
	//   ctx := context.Background()
	//   value := make([]byte, 256)
	//
	//   const preloadKeys = 5000
	//   keys := make([][]byte, preloadKeys)
	//   for i := range keys {
	//       keys[i] = []byte(fmt.Sprintf("bench:preload:%d", i))
	//       if _, err := c.Put(ctx, keys[i], value, vclock.NewVectorClock()); err != nil {
	//           b.Fatalf("preload put: %v", err)
	//       }
	//   }
	//
	//   b.ResetTimer()
	//   for i := 0; i < b.N; i++ {
	//       if i%5 == 0 { // 20% writes.
	//           key := []byte(fmt.Sprintf("bench:mix:w:%d", i))
	//           if _, err := c.Put(ctx, key, value, vclock.NewVectorClock()); err != nil {
	//               b.Fatalf("put: %v", err)
	//           }
	//           continue
	//       }
	//       if _, err := c.Get(ctx, keys[i%preloadKeys]); err != nil {
	//           b.Fatalf("get: %v", err)
	//       }
	//   }
}
