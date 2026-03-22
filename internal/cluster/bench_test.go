package cluster

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"GoQuorum/internal/config"
	"GoQuorum/internal/vclock"
)

// newBenchCluster builds a 3-node integration cluster optimised for benchmarks
// (no read repair, sync writes disabled, small cache).
func newBenchCluster(b *testing.B) []*integrationNode {
	b.Helper()
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 2}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 0}
	// reuse the integration-test helper — it calls b.TempDir and b.Cleanup under the hood
	// because *testing.B embeds *testing.T-compatible methods via the testing.TB interface.
	return newIntegrationCluster(b, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)
}

// newBenchClusterSingleNode builds a single-node cluster for local-only benchmarks.
func newBenchClusterSingleNode(b *testing.B) []*integrationNode {
	b.Helper()
	quorumCfg := config.QuorumConfig{N: 1, R: 1, W: 1}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 0}
	return newIntegrationCluster(b, []string{"n1"}, quorumCfg, repairCfg)
}

// ── Single-node storage benchmarks ────────────────────────────────────────

// BenchmarkPut_SingleNode measures raw single-node put latency (no replication).
func BenchmarkPut_SingleNode(b *testing.B) {
	nodes := newBenchClusterSingleNode(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("bench-put-%d", i)
		_, err := coord.Put(ctx, key, []byte("value"), vclock.NewVectorClock())
		if err != nil {
			b.Fatalf("Put: %v", err)
		}
	}
}

// BenchmarkGet_SingleNode measures raw single-node get latency (key pre-written).
func BenchmarkGet_SingleNode(b *testing.B) {
	nodes := newBenchClusterSingleNode(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	// Pre-populate keys.
	const keyCount = 1000
	for i := 0; i < keyCount; i++ {
		key := fmt.Sprintf("bench-get-%d", i)
		if _, err := coord.Put(ctx, key, []byte("value"), vclock.NewVectorClock()); err != nil {
			b.Fatalf("setup Put: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("bench-get-%d", i%keyCount)
		_, err := coord.Get(ctx, key)
		if err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// ── 3-node quorum benchmarks ───────────────────────────────────────────────

// BenchmarkPut_3NodeQuorum measures coordinator Put latency across 3 real HTTP nodes (W=2).
func BenchmarkPut_3NodeQuorum(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("qbench-put-%d", i)
		_, err := coord.Put(ctx, key, []byte("quorum-value"), vclock.NewVectorClock())
		if err != nil {
			b.Fatalf("Put: %v", err)
		}
	}
}

// BenchmarkGet_3NodeQuorum measures coordinator Get latency across 3 real HTTP nodes (R=2).
func BenchmarkGet_3NodeQuorum(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	const keyCount = 1000
	for i := 0; i < keyCount; i++ {
		key := fmt.Sprintf("qbench-get-%d", i)
		if _, err := coord.Put(ctx, key, []byte("quorum-value"), vclock.NewVectorClock()); err != nil {
			b.Fatalf("setup Put: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("qbench-get-%d", i%keyCount)
		_, err := coord.Get(ctx, key)
		if err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// ── Replication lag benchmark ──────────────────────────────────────────────

// BenchmarkReplicationLag measures the elapsed time between a Put returning and
// all N replicas confirming the data locally.  Uses N=3, W=1 so the coordinator
// returns as soon as the local write succeeds; then polls until all 3 nodes see
// the key.  The reported ns/op is the end-to-end replication lag.
func BenchmarkReplicationLag(b *testing.B) {
	quorumCfg := config.QuorumConfig{N: 3, R: 1, W: 1}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 0}
	nodes := newIntegrationCluster(b, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)
	coord := nodes[0].coordinator

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("lag-key-%d", i)
		start := time.Now()

		_, err := coord.Put(ctx, key, []byte("lag-value"), vclock.NewVectorClock())
		if err != nil {
			b.Fatalf("Put: %v", err)
		}

		// Poll until all 3 nodes have the key (max 500 ms).
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			found := 0
			for _, n := range nodes {
				if _, err := n.storage.GetRaw([]byte(key)); err == nil {
					found++
				}
			}
			if found == 3 {
				break
			}
			time.Sleep(100 * time.Microsecond)
		}

		b.ReportMetric(float64(time.Since(start).Nanoseconds()), "lag-ns/op")
	}
}

// ── Concurrent write throughput (QPS) ─────────────────────────────────────

// BenchmarkConcurrentPuts measures write throughput under concurrency.
// b.N iterations are distributed across GOMAXPROCS goroutines; b.ReportMetric
// reports QPS (operations per second).
func BenchmarkConcurrentPuts(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	var counter atomic.Int64
	b.SetParallelism(8) // 8 goroutines per CPU

	b.ResetTimer()
	b.ReportAllocs()

	start := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			key := fmt.Sprintf("conc-put-%d", n)
			_, err := coord.Put(ctx, key, []byte("concurrent-value"), vclock.NewVectorClock())
			if err != nil {
				b.Errorf("Put: %v", err)
			}
		}
	})
	elapsed := time.Since(start).Seconds()
	ops := float64(b.N)
	b.ReportMetric(ops/elapsed, "writes/sec")
}

// BenchmarkConcurrentGets measures read throughput under concurrency.
func BenchmarkConcurrentGets(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	// Pre-populate.
	const keyCount = 500
	for i := 0; i < keyCount; i++ {
		key := fmt.Sprintf("conc-get-%d", i)
		if _, err := coord.Put(ctx, key, []byte("val"), vclock.NewVectorClock()); err != nil {
			b.Fatalf("setup Put: %v", err)
		}
	}

	var counter atomic.Int64
	b.SetParallelism(8)

	b.ResetTimer()
	b.ReportAllocs()

	start := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			key := fmt.Sprintf("conc-get-%d", n%keyCount)
			_, err := coord.Get(ctx, key)
			if err != nil {
				b.Errorf("Get: %v", err)
			}
		}
	})
	elapsed := time.Since(start).Seconds()
	b.ReportMetric(float64(b.N)/elapsed, "reads/sec")
}

// ── Batch operations ───────────────────────────────────────────────────────

// BenchmarkBatchPut measures coordinator BatchPut throughput (3-node quorum).
func BenchmarkBatchPut(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	const batchSize = 10

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		items := make([]BatchPutItem, batchSize)
		for j := 0; j < batchSize; j++ {
			items[j] = BatchPutItem{
				Key:     fmt.Sprintf("batch-%d-%d", i, j),
				Value:   []byte("batch-value"),
				Context: vclock.NewVectorClock(),
			}
		}
		results := coord.BatchPut(ctx, items)
		for _, r := range results {
			if r.Error != nil {
				b.Fatalf("BatchPut %s: %v", r.Key, r.Error)
			}
		}
	}
	b.ReportMetric(float64(b.N*batchSize), "total-keys")
}

// BenchmarkBatchGet measures coordinator BatchGet throughput (3-node quorum).
func BenchmarkBatchGet(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	const batchSize = 10
	const keyCount = 100

	// Pre-populate.
	for i := 0; i < keyCount; i++ {
		key := fmt.Sprintf("bget-%d", i)
		if _, err := coord.Put(ctx, key, []byte("val"), vclock.NewVectorClock()); err != nil {
			b.Fatalf("setup Put: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		keys := make([]string, batchSize)
		for j := 0; j < batchSize; j++ {
			keys[j] = fmt.Sprintf("bget-%d", (i*batchSize+j)%keyCount)
		}
		results := coord.BatchGet(ctx, keys)
		for _, r := range results {
			if r.Error != nil {
				b.Fatalf("BatchGet %s: %v", r.Key, r.Error)
			}
		}
	}
	b.ReportMetric(float64(b.N*batchSize), "total-keys")
}

// ── Put + Get round-trip latency ──────────────────────────────────────────

// BenchmarkRoundTrip_3NodeQuorum measures the full write-then-read latency
// for the same key through the 3-node quorum path.
func BenchmarkRoundTrip_3NodeQuorum(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("rt-%d", i)
		vc, err := coord.Put(ctx, key, []byte("round-trip-value"), vclock.NewVectorClock())
		if err != nil {
			b.Fatalf("Put: %v", err)
		}
		_, err = coord.Get(ctx, key)
		if err != nil {
			b.Fatalf("Get after Put (vc=%v): %v", vc, err)
		}
	}
}

// ── Mixed read/write workload ─────────────────────────────────────────────

// BenchmarkMixedWorkload_80R20W simulates a typical read-heavy workload:
// 80% Gets, 20% Puts across a shared key space.
func BenchmarkMixedWorkload_80R20W(b *testing.B) {
	nodes := newBenchCluster(b)
	coord := nodes[0].coordinator
	ctx := context.Background()

	const keyCount = 200
	for i := 0; i < keyCount; i++ {
		key := fmt.Sprintf("mix-%d", i)
		if _, err := coord.Put(ctx, key, []byte("initial"), vclock.NewVectorClock()); err != nil {
			b.Fatalf("setup: %v", err)
		}
	}

	var writeOps, readOps atomic.Int64
	var mu sync.Mutex
	_ = mu

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("mix-%d", i%keyCount)
			if i%5 == 0 { // 20% writes
				coord.Put(ctx, key, []byte("updated"), vclock.NewVectorClock()) //nolint:errcheck
				writeOps.Add(1)
			} else { // 80% reads
				coord.Get(ctx, key) //nolint:errcheck
				readOps.Add(1)
			}
			i++
		}
	})

	b.ReportMetric(float64(writeOps.Load()), "write-ops")
	b.ReportMetric(float64(readOps.Load()), "read-ops")
}
