# GoQuorum

A distributed key-value store with quorum-based replication, inspired by Amazon's Dynamo.

## Features

- **Quorum-based replication** - Configurable N/R/W parameters for tunable consistency
- **Vector clocks** - Causal consistency and conflict detection
- **Consistent hashing** - Automatic data partitioning with virtual nodes
- **Read repair** - Probabilistic background repair for eventual consistency
- **Anti-entropy** - Merkle tree-based synchronization
- **Failure detection** - Heartbeat-based peer health monitoring
- **gRPC API** - High-performance API with HTTP/JSON gateway
- **Gossip protocol** - Decentralized membership and failure propagation
- **Network partition detection** - Heuristic detection with metrics and logging
- **Observability** - Prometheus metrics, Loki log aggregation, Grafana dashboards

## Architecture

GoQuorum implements a masterless, peer-to-peer architecture where:
- Data is partitioned using consistent hashing with virtual nodes
- Each write is replicated to N nodes (configurable)
- Reads require R successful responses for quorum
- Writes require W successful responses for quorum
- Conflicts are resolved using vector clocks
- Failed nodes are detected via heartbeat mechanism

## Quick Start

### Single Node

```bash
# Build
go build -o bin/quorum ./cmd/quorum

# Run with default config
./bin/quorum -config config/cluster-node1.yaml
```

### 3-Node Cluster (Docker Compose)

```bash
# Start cluster with Prometheus, Loki, Promtail, and Grafana
docker-compose up -d

# Check cluster health
curl http://localhost:8080/health
curl http://localhost:8081/health
curl http://localhost:8082/health
```

## API Usage

### gRPC API

```go
import pb "GoQuorum/api/proto/goquorum"

// Put a key-value pair
req := &pb.PutRequest{
    Key:   []byte("user:123"),
    Value: []byte(`{"name":"Alice"}`),
}
resp, err := client.Put(ctx, req)

// Get a value
getReq := &pb.GetRequest{
    Key: []byte("user:123"),
}
getResp, err := client.Get(ctx, getReq)
```

### HTTP/JSON Gateway

```bash
# Put
curl -X POST http://localhost:8080/v1/kv \
  -H "Content-Type: application/json" \
  -d '{"key":"dXNlcjoxMjM=","value":"eyJuYW1lIjoiQWxpY2UifQ=="}'

# Get
curl http://localhost:8080/v1/kv/dXNlcjoxMjM=

# Delete
curl -X DELETE http://localhost:8080/v1/kv/dXNlcjoxMjM=
```

## Configuration

Configuration is via YAML file. Key parameters:

```yaml
node:
  node_id: "node1"
  data_dir: "./data/node1"

cluster:
  listen_addr: "localhost:7070"
  members:
    - id: "node1"
      addr: "localhost:7070"
    - id: "node2"
      addr: "localhost:7071"
    - id: "node3"
      addr: "localhost:7072"

quorum:
  n: 3  # Replication factor
  r: 2  # Read quorum
  w: 2  # Write quorum

storage:
  max_siblings: 10
  sibling_warning_threshold: 5
  tombstone_ttl: 168h  # 7 days
```

See `config/cluster-node1.yaml` for full example.

## Ports

- **7070-7072** - gRPC (node-to-node and client)
- **8080-8082** - HTTP/JSON gateway
- **9090** - Prometheus (metrics)
- **3000** - Grafana (dashboards)
- **3100** - Loki (log aggregation)

## Development

### Prerequisites

- Go 1.21+
- Protocol Buffers compiler
- Buf (for proto generation)

### Build

```bash
# Generate protobuf code
buf generate

# Build binary
go build -o bin/quorum ./cmd/quorum

# Run tests
go test ./...
```

### Project Structure

```
GoQuorum/
├── api/proto/          # Protocol buffer definitions
├── cmd/quorum/         # Main application entry point
├── config/             # Sample configuration files
├── internal/
│   ├── cluster/        # Cluster management, hash ring, coordinator
│   ├── common/         # Common types and errors
│   ├── config/         # Configuration structs
│   ├── observability/  # Logging and metrics
│   ├── server/         # gRPC server and API implementations
│   ├── storage/        # Storage engine (Pebble)
│   └── vclock/         # Vector clock implementation
└── pkg/client/         # Go client library
```

## Performance

### Live-cluster benchmark (docker-compose)

The primary benchmark tool connects over real gRPC to a running cluster and
measures P50 / P90 / P99 / P99.9 latency plus QPS for each workload.

```bash
# Start the cluster first
docker-compose up -d

# Run all workloads (30s each, 16 concurrent workers)
go run ./benchmarks/ \
  -nodes localhost:7071,localhost:7072,localhost:7073 \
  -concurrency 16 \
  -duration 30s

# Options
#   -nodes         comma-separated gRPC addresses  (default: localhost:7071,7072,7073)
#   -concurrency   worker count per workload        (default: 16)
#   -duration      measurement window per workload  (default: 30s)
#   -warmup        warmup window before each run    (default: 5s)
#   -value-size    payload bytes                    (default: 256)
#   -preload-keys  keys pre-populated for reads     (default: 5000)
#   -skip-lag      skip replication-lag workload
```

**Workloads measured:**

| Workload | What it measures |
|---|---|
| Put (N=3, W=2) | Write latency + QPS through quorum coordinator |
| Get (N=3, R=2) | Read latency + QPS (parallel reads, first R win) |
| Round-trip Put+Get | End-to-end write-then-read latency |
| Mixed 80R/20W | Throughput under realistic read-heavy load |
| Replication lag | Time from W=2 Put return → all 3 nodes readable |

Results include P50, P90, P99, P99.9, min, max, total ops, and QPS per workload.

**Results** — AMD Ryzen 7 PRO 8840HS, Docker (localhost), 16 workers, 256-byte values, sync writes on:

| Workload | QPS | P50 | P90 | P99 | P99.9 |
|---|---|---|---|---|---|
| Put (N=3, W=2) | 6,727 | 1.66 ms | 2.77 ms | 10.58 ms | 15.58 ms |
| Get (N=3, R=2) | 12,727 | 1.04 ms | 2.19 ms | 4.38 ms | 7.26 ms |
| Round-trip Put+Get | 1,496 | 9.83 ms | 11.31 ms | 14.76 ms | 200.82 ms |
| Mixed 80R/20W | 7,337 | 1.49 ms | 4.63 ms | 8.92 ms | 15.11 ms |
| Replication lag¹ | 1,826 | 1.54 ms | 2.87 ms | 5.26 ms | 8.78 ms |

¹ Replication lag = time from W=2 Put returning until all 3 nodes can serve a Get for that key (8 workers, loopback network).

---

### In-process microbenchmarks (no docker required)

For fast iteration during development — uses real Pebble storage and in-process
`httptest.Server` (no TCP, sync writes off):

```bash
go test -bench=. -benchmem -benchtime=3s ./internal/cluster/
```

Sample results on AMD Ryzen 7 PRO 8840HS (16 threads):

| Operation | P50 latency | QPS |
|---|---|---|
| Single-node Put | ~14.6 µs | — |
| Single-node Get | ~6.3 µs | — |
| 3-node quorum Put (W=2) | ~104 µs | — |
| 3-node quorum Get (R=2) | ~88 µs | — |
| Round-trip Put+Get | ~197 µs | — |
| Concurrent writes | — | ~34,000/sec |
| Concurrent reads | — | ~39,500/sec |
| Mixed 80R/20W | — | ~32,000 ops/sec |
| Replication lag | ~93 µs median | — |

> These numbers reflect algorithm + serialization overhead only (no real network,
> no fsync). Live-cluster numbers over a LAN with sync writes enabled will be
> significantly higher.

## Monitoring

Access Grafana dashboards at http://localhost:3000 (admin/admin) to view:
- Request rates and latencies
- Quorum success/failure rates
- Storage metrics
- Cluster health
- Vector clock sizes
- Partition detection events (suspected/healed)

Prometheus metrics: http://localhost:8080/metrics
Loki log explorer: http://localhost:3000/explore (select Loki datasource)

Both Prometheus and Loki are auto-provisioned as Grafana datasources when using Docker Compose.

## License

MIT
