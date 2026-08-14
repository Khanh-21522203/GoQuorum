package benchmarks

import "testing"

// BenchmarkRoundTrip measures combined Put+Get latency for a fresh key per
// iteration (N=3, W=2, R=2), mirroring the v1 live-cluster tool's
// workloadRoundTrip (GoQuorum/benchmarks/main.go).
func BenchmarkRoundTrip(b *testing.B) {
	cluster := newBenchCluster(b, 3)
	_ = cluster

	// Intended flow:
	//
	//   c, err := cluster.Nodes[0].Client()
	//   ctx := context.Background()
	//   value := make([]byte, 256)
	//
	//   b.ResetTimer()
	//   for i := 0; i < b.N; i++ {
	//       key := []byte(fmt.Sprintf("bench:rt:%d", i))
	//       if _, err := c.Put(ctx, key, value, vclock.NewVectorClock()); err != nil {
	//           b.Fatalf("put: %v", err)
	//       }
	//       if _, err := c.Get(ctx, key); err != nil {
	//           b.Fatalf("get: %v", err)
	//       }
	//   }
}
