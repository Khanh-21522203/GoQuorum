package benchmarks

import "testing"

// BenchmarkReplicationLag measures how long after a Put returns (W=2) a
// Get against a different coordinator node also observes the write,
// mirroring the v1 live-cluster tool's workloadReplicationLag
// (GoQuorum/benchmarks/main.go). Unlike the other benchmarks here, the
// measured quantity is not the loop iteration's own wall time but a
// polling delay recorded per iteration, so a real implementation should
// report it via b.ReportMetric rather than relying on the default ns/op.
func BenchmarkReplicationLag(b *testing.B) {
	cluster := newBenchCluster(b, 3)
	_ = cluster

	// Intended flow:
	//
	//   writer, err := cluster.Nodes[0].Client()
	//   reader, err := cluster.Nodes[1].Client()
	//   ctx := context.Background()
	//   value := make([]byte, 256)
	//
	//   lags := make([]time.Duration, 0, b.N)
	//   for i := 0; i < b.N; i++ {
	//       key := []byte(fmt.Sprintf("bench:lag:%d", i))
	//       if _, err := writer.Put(ctx, key, value, vclock.NewVectorClock()); err != nil {
	//           b.Fatalf("put: %v", err)
	//       }
	//       start := time.Now()
	//       for {
	//           if _, err := reader.Get(ctx, key); err == nil {
	//               lags = append(lags, time.Since(start))
	//               break
	//           }
	//           if time.Since(start) > 200*time.Millisecond {
	//               b.Fatalf("replication did not converge for key %d", i)
	//           }
	//       }
	//   }
	//   // Report mean/percentile lag via b.ReportMetric.
}
