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
- **Observability** - Prometheus metrics, structured logging, health endpoints

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
# Start cluster with Prometheus and Grafana
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
- **9090** - Prometheus (monitoring)
- **3000** - Grafana (dashboards)

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

## Monitoring

Access Grafana dashboards at http://localhost:3000 (admin/admin) to view:
- Request rates and latencies
- Quorum success/failure rates
- Storage metrics
- Cluster health
- Vector clock sizes

Prometheus metrics available at http://localhost:8080/metrics

## License

MIT
