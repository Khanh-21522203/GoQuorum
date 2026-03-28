# GoQuorum — Design Index

GoQuorum is a Dynamo-style distributed key-value store written in Go. It runs as a long-lived server process (`cmd/quorum/main.go`) that exposes gRPC and HTTP/JSON-gateway APIs to clients. Nodes form a cluster using consistent hashing, replicate data with tunable N/R/W quorum semantics, and maintain causal consistency via vector clocks. Background subsystems (anti-entropy, read repair, hinted handoff, TTL sweeper) keep replicas converging toward consistency. Storage is backed by CockroachDB's Pebble LSM engine.

## Mental Map

```
┌─ Data Plane ──────────────────────────────────┐  ┌─ Storage ──────────────────────────────────────┐
│ Owns: quorum reads/writes, key routing        │  │ Owns: LSM persistence, sibling sets, GC        │
│ Entry: cluster/coordinator.go                 │  │ Entry: storage/engine.go                       │
│ Key:   cluster/hashring.go                    │  │ Key:   storage/encoding.go, storage/pebble.go  │
│ Uses:  Storage, Shared                        │  └────────────────────────────────────────────────┘
└───────────────────────────────────────────────┘
┌─ Cluster Membership ──────────────────────────┐  ┌─ Consistency Repairs ──────────────────────────┐
│ Owns: peer state, gossip, heartbeat tracking  │  │ Owns: read repair, anti-entropy, Merkle sync   │
│ Entry: cluster/gossip.go                      │  │ Key:   cluster/read_repair.go                  │
│ Key:   cluster/membership.go,                 │  │        cluster/anti_entropy.go,                │
│        cluster/failure_detector.go            │  │        cluster/merkle_tree.go                  │
│ Uses:  Data Plane                             │  │ Uses:  Data Plane, Storage                     │
└───────────────────────────────────────────────┘  └────────────────────────────────────────────────┘
┌─ Server API ──────────────────────────────────┐  ┌─ Durability ───────────────────────────────────┐
│ Owns: gRPC+HTTP routing, rate limiting        │  │ Owns: hinted handoff, TTL key expiry           │
│ Entry: server/server.go                       │  │ Key:   cluster/hinted_handoff.go,              │
│ Key:   server/client_api.go, admin_api.go,    │  │        cluster/ttl_sweeper.go                  │
│        server/internal_api.go                 │  │ Uses:  Storage, Data Plane                     │
│ Uses:  Data Plane, Cluster Membership         │  └────────────────────────────────────────────────┘
└───────────────────────────────────────────────┘
┌─ Cluster Lifecycle ───────────────────────────┐  ┌─ Observability ────────────────────────────────┐
│ Owns: bootstrap quorum, join/leave, shutdown  │  │ Owns: Prometheus metrics, logging, probes      │
│ Key:   cluster/bootstrap.go,                  │  │ Entry: observability/logger.go                 │
│        cluster/graceful_shutdown.go           │  │ Key:   server/health.go, server/debug.go       │
│ Uses:  Cluster Membership, Data Plane         │  └────────────────────────────────────────────────┘
└───────────────────────────────────────────────┘
┌─ Configuration ───────────────────────────────┐  ┌─ Shared ───────────────────────────────────────┐
│ Owns: YAML loading, validation, presets       │  │ Owns: vector clocks, common error types        │
│ Entry: config/config.go                       │  │ Key:   vclock/vclock.go, vclock/compare.go,    │
└───────────────────────────────────────────────┘  │        internal/common/, pkg/client/           │
                                                   └────────────────────────────────────────────────┘
┌─ TLS Security ────────────────────────────────┐  ┌─ Backup & Restore ─────────────────────────────┐  ┌─ CLI Tool ───────────────────────────┐
│ Owns: mTLS for gRPC and inter-node HTTP       │  │ Owns: Pebble checkpoints, tar+gzip, SHA256     │  │ Owns: quorumctl data ops and admin   │
│ Key:   security/tls.go                        │  │ Entry: backup/backup.go                        │  │ Entry: cmd/quorumctl/main.go         │
│ Uses:  Configuration                          │  │ Uses:  Storage                                 │  │ Uses:  Server API                    │
└───────────────────────────────────────────────┘  └────────────────────────────────────────────────┘  └──────────────────────────────────────┘
```

## Feature Matrix

| Feature | Description | File | Status |
|---------|-------------|------|--------|
| Storage Engine | Pebble LSM persistence, sibling reconciliation, tombstone GC, TTL, CRC32 integrity | [storage-engine.md](storage-engine.md) | Stable |
| Vector Clocks | Lamport vector clocks for causality tracking, conflict detection, and pruning | [vector-clocks.md](vector-clocks.md) | Stable |
| Consistent Hashing | Virtual-node hash ring for deterministic key-to-node mapping | [consistent-hashing.md](consistent-hashing.md) | Stable |
| Quorum Replication | Coordinator orchestrating N/R/W quorum reads/writes with sloppy quorum | [quorum-replication.md](quorum-replication.md) | Stable |
| Gossip & Membership | Gossip protocol for cluster state propagation and peer health tracking | [gossip-membership.md](gossip-membership.md) | Stable |
| Failure Detection | Heartbeat-based liveness monitoring with partition detection callbacks | [failure-detection.md](failure-detection.md) | Stable |
| Read Repair | Post-read consistency repair pushing merged siblings to stale replicas | [read-repair.md](read-repair.md) | Stable |
| Anti-Entropy | Background Merkle tree sync reconciling all keys across replicas | [anti-entropy.md](anti-entropy.md) | Stable |
| Hinted Handoff | In-memory write buffer for offline nodes, replayed on recovery | [hinted-handoff.md](hinted-handoff.md) | Stable |
| TTL & Tombstone GC | Key expiration via TTLSweeper and Pebble-level tombstone cleanup | [ttl-tombstone-gc.md](ttl-tombstone-gc.md) | Stable |
| Client API | gRPC + HTTP/JSON gateway for data plane and admin operations | [client-api.md](client-api.md) | Stable |
| Cluster Lifecycle | Bootstrap sequence, dynamic join/leave, and graceful shutdown | [cluster-lifecycle.md](cluster-lifecycle.md) | Stable |
| Configuration | YAML config schema, defaults, validation, and preset builders | [configuration.md](configuration.md) | Stable |
| TLS Security | TLS and mutual TLS for gRPC and inter-node HTTP connections | [tls-security.md](tls-security.md) | Stable |
| Backup & Restore | Checkpoint-based snapshots with SHA256 integrity verification | [backup-restore.md](backup-restore.md) | Stable |
| Observability | Prometheus metrics, structured logging, health probes, debug endpoints | [observability.md](observability.md) | Stable |
| CLI Tool | `quorumctl` — data operations and cluster inspection from the terminal | [cli-tool.md](cli-tool.md) | Stable |

## Cross-Cutting Concerns

### Causal Consistency Model

Every stored value (`Sibling`) carries a `VectorClock`. When concurrent writes produce multiple siblings, they coexist in the `SiblingSet` until a client resolves the conflict by reading all siblings and writing back a merged value with a context that dominates all siblings. `HappensAfter` / `Concurrent` comparisons in `internal/vclock/compare.go` are the single source of truth for ordering decisions across the storage engine, read repair, and anti-entropy.

### Error Propagation

Internal errors use typed domain errors (`common.QuorumError`, `common.ErrStorageFull`, `common.ErrCorruptedData`). The server layer (`internal/server/client_api.go:convertError`) maps these to gRPC status codes. Replica-level errors are aggregated in `QuorumError.ReplicaErrors` for observability.

### Prometheus Metrics

Each subsystem creates its own `XxxMetrics` struct via a `NewXxxMetrics(registry)` constructor. All are registered on a single shared registry initialized in `main.go`. Metrics are exposed at `GET /v1/admin/metrics` (gRPC-gateway) and at `GET /metrics` (HTTP). See [observability.md](observability.md) for the full metric inventory.

### Internal vs. External API

Client-facing operations travel over gRPC on `:7070` (configurable). Node-to-node communication (replication, heartbeats, merkle root exchange, gossip) uses HTTP/JSON on `:8080` via paths under `/internal/`. Despite being named `GRPCClient`, `internal/cluster/rpc_client.go` uses HTTP/JSON transport for inter-node calls.

### Data Size Limits

Enforced at the encoding layer (`internal/storage/encoding.go`): keys ≤ 64 KB (`MaxKeySize`), values ≤ 1 MB (`MaxValueSize`), siblings per key ≤ 65535 (`MaxSiblings`). The storage engine additionally enforces `StorageOptions.MaxSiblings` (default 100) with pruning.

### Observability Stack (Docker Compose)

`docker-compose.yaml` provides a 3-node cluster with Prometheus (port 9090), Loki (3100), Promtail, and Grafana (3000). Grafana datasources are auto-provisioned; dashboards must be built manually.

## Notes

None.
