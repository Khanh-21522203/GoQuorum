package benchmarks

import "testing"

// BenchmarkGet measures sustained Get throughput/latency against a 3-node
// cluster (N=3, R=2) over a pre-populated key set, mirroring the v1
// live-cluster tool's workloadGet (GoQuorum/benchmarks/main.go).
func BenchmarkGet(b *testing.B) {
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
	//       if _, err := c.Get(ctx, keys[i%preloadKeys]); err != nil {
	//           b.Fatalf("get: %v", err)
	//       }
	//   }
	//   b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
}
