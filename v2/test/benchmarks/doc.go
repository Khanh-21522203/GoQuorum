// Package benchmarks holds Benchmark* skeletons mirroring the v1
// live-cluster benchmark tool (GoQuorum/benchmarks/main.go): Put, Get, a
// round-trip Put+Get, an 80/20 read/write mix, and replication lag. Each
// benchmark currently calls b.Skip (via the shared newBenchCluster
// helper), since it is driven through test/harness, which cannot start a
// real node until infra/storage/pebble and infra/transport/httprpc are
// implemented.
package benchmarks
