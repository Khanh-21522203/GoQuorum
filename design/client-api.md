---
name: Client API
description: gRPC + HTTP/JSON gateway for client-facing read/write/delete/batch operations and admin endpoints
type: project
---

# Client API

## Overview

GoQuorum exposes two gRPC services defined in `api/proto/goquorum.proto`, both bridged to HTTP/JSON via grpc-gateway:

- **`GoQuorum`** — data plane: Get, Put, Delete, BatchGet, BatchPut.
- **`GoQuorumAdmin`** — control plane: Health, ClusterInfo, GetMetrics, KeyInfo, TriggerCompaction.

The server-side implementation lives in `internal/server/client_api.go`, `internal/server/backup_api.go`, `internal/server/debug.go`, `internal/server/health.go`.

## gRPC Addresses

| Interface | Default Port | Config field |
|---|---|---|
| gRPC (clients + admin) | `:7070` | `server.grpc_addr` |
| HTTP gateway | `:8080` | `server.http_addr` |

## GoQuorum Service

### Get

```
rpc Get(GetRequest) returns (GetResponse)
HTTP: GET /v1/keys/{key}
```

**Request fields**: `key`, `r_quorum` (override R, 0=use config), `timeout_ms` (0=no override).

**Response**: `siblings[]` (list of `Sibling`), `found bool`.

`Sibling` message:
```
value     bytes
context   Context   — vector clock for read-modify-write
tombstone bool
timestamp int64     — Unix seconds
```

`Context` message:
```
entries[] { node_id string, counter uint64 }
```

Server handler (`ClientAPI.Get`, `client_api.go`):
1. Validates key: non-empty, ≤ 64 KB.
2. Applies `timeout_ms` if set.
3. Calls `coordinator.Get(ctx, key, options)`.
4. Filters tombstones from result.
5. Maps errors: `QuorumError` → gRPC `Unavailable`, deadline → `DeadlineExceeded`, disk full → `ResourceExhausted`.

### Put

```
rpc Put(PutRequest) returns (PutResponse)
HTTP: PUT /v1/keys/{key}
```

**Request fields**: `key`, `value` (bytes, ≤1 MB), `context` (optional prior `Context`), `w_quorum`, `timeout_ms`, `ttl_seconds` (0=no TTL).

**Response**: `context Context` — new vector clock after write (pass back on next write to the same key).

Server handler:
1. Validates key ≤ 64 KB, value ≤ 1 MB.
2. Converts `Context.entries` map → internal `VectorClock` via `contextToVClock`.
3. Calls `coordinator.Put(ctx, key, value, vclock, options)`.
4. Returns new vector clock as `Context` via `vclockToContext`.

### Delete

```
rpc Delete(DeleteRequest) returns (DeleteResponse)
HTTP: DELETE /v1/keys/{key}
```

**Request fields**: `key`, `context` (required — cannot delete without a vector clock from a prior read), `w_quorum`, `timeout_ms`.

Server handler:
1. Requires `context` to be non-nil (returns `InvalidArgument` if missing).
2. Calls `coordinator.Delete(ctx, key, vclock, options)`.

### BatchGet

```
rpc BatchGet(BatchGetRequest) returns (BatchGetResponse)
HTTP: POST /v1/batch/get
```

**Request**: `keys []string`.
**Response**: `results[] { key, siblings[], error string }` — per-key error strings for partial failures.

### BatchPut

```
rpc BatchPut(BatchPutRequest) returns (BatchPutResponse)
HTTP: POST /v1/batch/put
```

**Request**: `items[] { key, value, context }`.
**Response**: `results[] { key, context, error string }`.

No cross-key atomicity. Each key is independently committed.

## GoQuorumAdmin Service

### Health

```
rpc Health(HealthRequest) returns (HealthResponse)
HTTP: GET /v1/admin/health
```

Implemented in `internal/server/health.go`. Returns:
- `status` (HEALTHY/DEGRADED/UNHEALTHY)
- `node_id`, `version`, `uptime_seconds`
- `checks map<string, CheckResult>` with entries for `storage`, `cluster`, `disk`

Also exposed as bare HTTP endpoints:
- `GET /health` → full JSON
- `GET /health/live` → 200 always (liveness probe)
- `GET /health/ready` → 200 if ready; 503 if not (readiness probe)

Readiness: `storage != nil && activePeerPct >= 50%`.

Disk health thresholds (via `syscall.Statfs`):
- `< 80% used` → healthy
- `80–95%` → degraded
- `≥ 95%` → unhealthy

### ClusterInfo

```
rpc ClusterInfo(ClusterInfoRequest) returns (ClusterInfoResponse)
HTTP: GET /v1/admin/cluster
```

Returns `node_id`, `peers[]` (ID, addr, status), `cluster_status`.

Also available as `GET /debug/cluster` returning `ClusterDebugInfo` JSON (peer list with LatencyP99Ms, heartbeat failure counts).

### GetMetrics

```
rpc GetMetrics(MetricsRequest) returns (MetricsResponse)
HTTP: GET /v1/admin/metrics
```

Returns Prometheus text-format metrics string.

### KeyInfo

```
rpc KeyInfo(KeyInfoRequest) returns (KeyInfoResponse)
HTTP: GET /v1/admin/keys/{key}
```

Returns `replicas[]` (which nodes hold the key and sibling metadata: timestamp, value size, tombstone), `preference_list` (NodeIDs in hash ring order).

### TriggerCompaction

```
rpc TriggerCompaction(CompactionRequest) returns (CompactionResponse)
HTTP: POST /v1/admin/compaction
```

Triggers a manual Pebble compaction. Returns `started bool`, `message string`.

## Internal HTTP Routes (node-to-node)

These are not part of the public API; they are consumed by `RPCClient` and `Gossip`:

| Route | Handler | Purpose |
|---|---|---|
| `POST /internal/replicate` | write replica handler | Receive a replicated write |
| `POST /internal/read` | read replica handler | Serve a remote read request |
| `POST /internal/heartbeat` | heartbeat handler | Liveness heartbeat |
| `POST /internal/merkle-root` | anti-entropy handler | Return local Merkle root |
| `POST /internal/notify-leaving` | leave handler | Node departure notification |
| `POST /internal/gossip/exchange` | gossip handler | Push-pull gossip exchange |

## Error Mapping (`client_api.go:convertError`)

| Internal error | gRPC status code |
|---|---|
| `QuorumError{QuorumNotReached}` | `Unavailable` |
| `QuorumError{AllReplicasUnavailable}` | `Unavailable` |
| `ErrKeyNotFound` | `NotFound` |
| `ErrStorageFull` | `ResourceExhausted` |
| `context.DeadlineExceeded` | `DeadlineExceeded` |
| Other | `Internal` |

Changes:

