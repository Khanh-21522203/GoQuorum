---
name: Observability
description: Prometheus metrics, structured logging, health probes, and debug endpoints
type: project
---

# Observability

## Overview

GoQuorum provides three observability layers:

1. **Prometheus metrics** — per-subsystem counters, gauges, and histograms.
2. **Structured logging** — via `internal/observability/logger.go`.
3. **Health probes + debug endpoints** — HTTP endpoints for operational visibility.

## Prometheus Metrics

Metrics are registered via `NewXxxMetrics(registry)` constructors in each subsystem. All use a shared `*prometheus.Registry` initialized in `main.go`.

### Subsystem Metrics Summary

| Subsystem | File | Key Metrics |
|---|---|---|
| Coordinator | `coordinator_metrics.go` | `ReadLatency`, `WriteLatency`, `ReadQuorumFailures`, `WriteQuorumFailures`, `HintedHandoffWrites`, `ReadSiblingsCount` |
| Storage | `storage/metric.go` | `ReadLatency`, `WriteLatency`, `BytesRead`, `BytesWritten`, `CorruptedReads`, `SiblingsPruned`, `TombstonesGCed`, `DiskFullErrors` |
| Membership | `membership_metrics.go` | `PeersTotal`, `PeersActive`, `PeersFailed`, `PeersRecovered`, `PeerStateChanges` |
| Failure Detector | `failure_detector_metrics.go` | `HeartbeatSuccess`, `HeartbeatFailure`, `NodeFailed`, `NodeRecovered`, `PartitionSuspected`, `PartitionHealed`, `PeerLatency` (per-peer histogram) |
| Anti-Entropy | `anti_entropy_metrics.go` | `ExchangesTotal`, `ExchangesFailed`, `KeysExchanged`, `BytesSent`, `BytesReceived`, `ScanDuration`, `MerkleTreeDiffNodes` |
| Read Repair | `read_repair_metrics.go` | `ReadRepairTriggered`, `ReadRepairSent`, `ReadRepairFailed`, `ReadRepairKeysRepaired`, `ReadRepairLatency` |

### Accessing Metrics

`GET /v1/admin/metrics` (gRPC-gateway) or `GET /metrics` (HTTP) — returns Prometheus text format. In the Docker Compose setup, Prometheus scrapes all three nodes on their HTTP ports.

### Key Histogram Buckets

| Metric | Bucket Type | Range |
|---|---|---|
| `coordinator.ReadLatency` | Exponential | 1ms – 16s |
| `coordinator.WriteLatency` | Exponential | 1ms – 16s |
| `coordinator.ReadSiblingsCount` | Linear | 1 – 10 (step 1) |
| `storage.ReadLatency` | Exponential | 10µs – 10s |
| `storage.WriteLatency` | Exponential | 100µs – 100s |
| `failure_detector.PeerLatency` | Exponential | (per peer, RTT distribution) |
| `anti_entropy.ScanDuration` | Exponential | 1s – 2^14s |
| `anti_entropy.MerkleTreeDiffNodes` | Linear | 0 – 200 (step 10) |
| `read_repair.ReadRepairLatency` | Exponential | 0.1ms – 2^14s |

## Structured Logging (`internal/observability/logger.go`)

Logger provides leveled structured output. Log level is configurable via `node.log_level` (debug/info/warn/error, default info). Components log at appropriate levels:
- `debug`: per-key operations, heartbeat details
- `info`: node state transitions, bootstrap progress, compaction events
- `warn`: quorum failures, repair failures, hint eviction
- `error`: storage I/O errors, corruption, fatal failures

In the Docker Compose stack, Promtail collects logs from containers and ships them to Loki for aggregation.

## Health Endpoints (`internal/server/health.go`)

| Route | Purpose | Response codes |
|---|---|---|
| `GET /health` | Full health check (storage + cluster + disk) | 200 healthy, 503 degraded/unhealthy |
| `GET /health/live` | Liveness probe | Always 200 |
| `GET /health/ready` | Readiness probe | 200 if ready, 503 if not |

Full health response (`HealthResponse`):
```json
{
  "status": "healthy",
  "node_id": "node1",
  "version": "0.1.0-mvp",
  "uptime_seconds": 3600,
  "checks": {
    "storage": {"status": "healthy", "latency_ms": 0.5},
    "cluster": {"status": "healthy", "peers_active": 2, "peers_total": 2},
    "disk": {"status": "healthy", "free_bytes": 10737418240, "total_bytes": 107374182400}
  }
}
```

**Readiness condition**: `storage != nil && (no membership OR activePeerPct >= 50%)`. Returns 503 during bootstrap and graceful shutdown drain.

## Debug Endpoints (`internal/server/debug.go`)

| Route | Returns |
|---|---|
| `GET /debug/cluster` | `ClusterDebugInfo`: node status, peers with LatencyP99Ms and heartbeat failures |
| `GET /debug/ring` | `RingDebugInfo`: virtual node counts, nodes with estimated key range percentage |

`ClusterDebugInfo` example:
```json
{
  "node_id": "node1",
  "status": "Active",
  "peers": [
    {"node_id": "node2", "addr": "node2:7070", "status": "Active",
     "last_seen": "2026-03-27T10:00:00Z", "latency_p99_ms": 1.2, "heartbeat_failures": 0}
  ]
}
```

`RingDebugInfo` example:
```json
{
  "vnodes_per_node": 256,
  "total_vnodes": 768,
  "nodes": [
    {"node_id": "node1", "vnodes": 256, "key_range_pct": 33.33}
  ]
}
```

## Grafana Dashboard (Docker Compose)

`docker-compose.yaml` starts:
- **Prometheus** (port 9090) scraping all three nodes
- **Loki** (port 3100) receiving logs from Promtail
- **Grafana** (port 3000, admin/admin) with auto-provisioned Prometheus + Loki datasources

Grafana dashboards are not pre-built in the repo — they need to be created manually using the available Prometheus metrics.

Changes:

