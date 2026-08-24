package benchmarks

import "testing"

// BenchmarkPut measures sustained Put throughput/latency against a 3-node
// cluster (N=3, W=2), mirroring the v1 live-cluster tool's workloadPut
// (GoQuorum/benchmarks/main.go).
func BenchmarkPut(b *testing.B) {
	cluster := newBenchCluster(b, 3)
	_ = cluster

	// Intended flow:
	//
	//   c, err := cluster.Nodes[0].Client()
	//   if err != nil {
	//       b.Fatalf("client: %v", err)
	//   }
	//   defer c.Close()
	//
	//   ctx := context.Background()
	//   value := make([]byte, 256)
	//
	//   b.ResetTimer()
	//   for i := 0; i < b.N; i++ {
	//       key := []byte(fmt.Sprintf("bench:put:%d", i))
	//       if _, err := c.Put(ctx, key, value, vclock.NewVectorClock()); err != nil {
	//           b.Fatalf("put: %v", err)
	//       }
	//   }
	//   b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
}
