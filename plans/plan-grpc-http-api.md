# Feature: gRPC + HTTP/JSON API

## 1. Purpose

The API layer exposes GoQuorum's functionality to external clients (via gRPC and HTTP/JSON REST) and to peer nodes (via an internal gRPC service). It is the entry point for all read, write, and delete operations, as well as admin and debug endpoints. A single binary serves all three services — client API, admin API, and internal node-to-node API — on a configurable gRPC port and HTTP gateway port.

## 2. Responsibilities

- **Client API (gRPC + HTTP)**: Handle `Get`, `Put`, and `Delete` requests from end clients; delegate to `Coordinator`; serialize/deserialize protobuf and JSON
- **Admin API (gRPC + HTTP)**: Handle `Health`, `ClusterInfo`, `GetMetrics`, `KeyInfo`, and `TriggerCompaction` calls; surface diagnostic information
- **Internal API (gRPC only)**: Handle inter-node RPCs: `Replicate`, `Read`, `Heartbeat`, `AntiEntropyExchange`, `GetMerkleRoot`; delegate to local storage, failure detector, and anti-entropy
- **gRPC-Gateway**: Automatically translate HTTP/JSON requests to gRPC and back using `grpc-gateway` annotations
- **Health endpoints**: Serve `/health`, `/health/live`, and `/health/ready` on the HTTP port
- **Metrics endpoint**: Serve `/metrics` in Prometheus exposition format
- **Debug endpoints**: Serve `/debug/cluster` and `/debug/ring` for operator inspection
- **Graceful shutdown**: Drain in-flight requests before stopping on SIGTERM/SIGINT

## 3. Non-Responsibilities

- Does not implement business logic (delegated to `Coordinator`, `storage.Engine`, etc.)
- Does not implement TLS/mTLS (see `plan-tls-security.md`)
- Does not implement rate limiting (see `plan-rate-limiting.md`)
- Does not implement the cluster health logic (delegated to `health.go`)

## 4. Architecture Design

```
                 Client
                   |
          gRPC :7070 / HTTP :8080
                   |
    +--------------+---------------+
    |         server.Server         |
    |                               |
    | gRPC mux:                     |
    |   GoQuorum (client API)       |---> Coordinator (Put/Get/Delete)
    |   GoQuorumAdmin (admin API)   |---> storage.Engine, membership, ring
    |   GoQuorumInternal (internal) |---> storage.Engine, failure detector, AE
    |                               |
    | HTTP mux (grpc-gateway):      |
    |   /v1/keys/*   → gRPC client  |
    |   /v1/admin/*  → gRPC admin   |
    |   /health      → health check |
    |   /metrics     → prometheus   |
    |   /debug/*     → debug info   |
    +--------------+----------------+

Proto services:
  goquorum.proto:   GoQuorum, GoQuorumAdmin
  internal.proto:   GoQuorumInternal
```

### Request Flow: Client Put
```
HTTP PUT /v1/keys/{key}  →  grpc-gateway  →  gRPC GoQuorum.Put
  →  server.ClientAPI.Put()
  →  Coordinator.Put(ctx, key, value)
  →  [quorum write to N replicas]
  →  return PutResponse{context: vclock bytes}
```

## 5. Core Data Structures (Go)

```go
package server

import (
    "net"
    "net/http"
    "google.golang.org/grpc"
    "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
    "goquorum/internal/cluster"
    "goquorum/internal/storage"
)

// Server bundles the gRPC and HTTP servers for a GoQuorum node.
type Server struct {
    config      ServerConfig
    grpcServer  *grpc.Server
    httpServer  *http.Server
    coordinator *cluster.Coordinator
    local       storage.Engine
    membership  *cluster.Membership
    ring        *cluster.HashRing
    detector    *cluster.FailureDetector
    antiEntropy *cluster.AntiEntropy
    health      *HealthChecker
    logger      *slog.Logger
    metrics     *ServerMetrics
}

// ServerConfig holds network configuration.
type ServerConfig struct {
    GRPCAddr    string `yaml:"grpc_addr" default:":7070"`
    HTTPAddr    string `yaml:"http_addr" default:":8080"`
    MaxRecvMsgSize int `yaml:"max_recv_msg_size_mb" default:"4"`
    MaxSendMsgSize int `yaml:"max_send_msg_size_mb" default:"4"`
}
```

## 6. Public Interfaces

```go
package server

// New constructs a Server wiring all dependencies.
func New(cfg ServerConfig, coordinator *cluster.Coordinator, /* ... */) *Server

// Start starts both the gRPC and HTTP listeners. Blocks until stopped.
func (s *Server) Start() error

// Stop gracefully drains in-flight requests and shuts down both servers.
func (s *Server) Stop(ctx context.Context) error

// ClientAPI is the gRPC handler for the GoQuorum service (client-facing).
type ClientAPI struct {
    proto.UnimplementedGoQuorumServer
    coordinator *cluster.Coordinator
    metrics     *ServerMetrics
}

// AdminAPI is the gRPC handler for the GoQuorumAdmin service.
type AdminAPI struct {
    proto.UnimplementedGoQuorumAdminServer
    local    storage.Engine
    ring     *cluster.HashRing
    members  *cluster.Membership
    metrics  *ServerMetrics
}

// InternalAPI is the gRPC handler for the GoQuorumInternal service.
type InternalAPI struct {
    proto.UnimplementedGoQuorumInternalServer
    local      storage.Engine
    detector   *cluster.FailureDetector
    antiEntropy *cluster.AntiEntropy
    metrics    *ServerMetrics
}
```

## 7. Internal Algorithms

### ClientAPI.Get
```
Get(ctx, req):
  if req.Key == "": return status.Error(InvalidArgument, "key required")
  sibs, vc, err = coordinator.Get(ctx, []byte(req.Key))
  if err == ErrNotFound:
    return nil, status.Error(NotFound, "key not found")
  if err != nil:
    return nil, mapError(err)

  resp = &proto.GetResponse{
    Siblings: toProtoSiblings(sibs),
    Context:  vc.MarshalBinary(),
  }
  return resp, nil
```

### ClientAPI.Put
```
Put(ctx, req):
  if req.Key == "" || req.Value == nil:
    return status.Error(InvalidArgument, "key and value required")
  vc, err = coordinator.Put(ctx, []byte(req.Key), req.Value)
  if err != nil:
    return nil, mapError(err)
  return &proto.PutResponse{Context: vc.MarshalBinary()}, nil
```

### ClientAPI.Delete
```
Delete(ctx, req):
  if req.Key == "" || req.Context == nil:
    return status.Error(InvalidArgument, "key and context required")
  vc, err = vclock.UnmarshalBinary(req.Context)
  if err != nil:
    return status.Error(InvalidArgument, "invalid context")
  err = coordinator.Delete(ctx, []byte(req.Key), vc)
  if err != nil:
    return nil, mapError(err)
  return &proto.DeleteResponse{}, nil
```

### InternalAPI.Replicate
```
Replicate(ctx, req):
  sibSet = fromProtoSiblingSet(req.Siblings)
  err = local.Put([]byte(req.Key), sibSet)
  if err != nil:
    return nil, status.Error(Internal, err.Error())
  return &proto.ReplicateResponse{}, nil
```

### InternalAPI.Heartbeat
```
Heartbeat(ctx, req):
  status = fromProtoNodeStatus(req.Status)
  detector.RecordHeartbeat(req.NodeId, status)
  localStatus = buildLocalStatus()
  return &proto.HeartbeatResponse{Status: toProtoNodeStatus(localStatus)}, nil
```

### mapError (error code translation)
```
mapError(err):
  switch err:
  case ErrNotFound:    return status.Error(NotFound, ...)
  case ErrQuorum:      return status.Error(Unavailable, ...)
  case ErrKeyTooLarge: return status.Error(InvalidArgument, ...)
  case ErrValueTooLarge: return status.Error(InvalidArgument, ...)
  default:             return status.Error(Internal, ...)
```

## 8. Persistence Model

The API layer is stateless. No data is stored in the server layer; all persistence is handled by `storage.Engine`.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| gRPC server goroutine pool | gRPC handles each request in its own goroutine (default behavior) |
| HTTP/2 multiplexing | grpc-gateway shares the HTTP/2 connection; each request is independent |
| Graceful shutdown context | `grpcServer.GracefulStop()` waits for in-flight RPCs to complete |
| No shared mutable state in handlers | Handlers are stateless — all coordination flows through `Coordinator` |

## 10. Configuration

```go
type ServerConfig struct {
    GRPCAddr       string        `yaml:"grpc_addr" default:":7070"`
    HTTPAddr       string        `yaml:"http_addr" default:":8080"`
    MaxRecvMsgMB   int           `yaml:"max_recv_msg_size_mb" default:"4"`
    MaxSendMsgMB   int           `yaml:"max_send_msg_size_mb" default:"4"`
    ShutdownTimeout time.Duration `yaml:"shutdown_timeout" default:"30s"`
    // Interceptors
    EnableRequestLog  bool `yaml:"enable_request_log" default:"false"`
    EnableReflection  bool `yaml:"enable_reflection" default:"true"`
}
```

## 11. Observability

- `goquorum_grpc_requests_total{method,code}` — gRPC request counter per method and status code
- `goquorum_grpc_request_duration_seconds{method}` — gRPC latency histogram per method
- `goquorum_http_requests_total{path,method,status}` — HTTP request counter
- `goquorum_http_request_duration_seconds{path}` — HTTP latency histogram
- Request logging interceptor logs method, latency, and status at DEBUG level (when enabled)
- Startup log at INFO includes gRPC and HTTP bind addresses
- `/health/live` returns 200 if process is running
- `/health/ready` returns 200 if coordinator, storage, and membership are initialized

## 12. Testing Strategy

- **Unit tests**:
  - `TestClientAPIGetSuccess`: mock coordinator returns siblings, assert proto response correct
  - `TestClientAPIGetNotFound`: coordinator returns ErrNotFound, assert gRPC NotFound status
  - `TestClientAPIPutMissingKey`: empty key, assert InvalidArgument
  - `TestClientAPIDeleteMissingContext`: nil context, assert InvalidArgument
  - `TestInternalAPIReplicateStoresLocally`: Replicate called, assert local engine Put called
  - `TestInternalAPIHeartbeatRecorded`: Heartbeat called, assert detector.RecordHeartbeat called
  - `TestMapError`: each error type maps to correct gRPC status code
- **Integration tests**:
  - `TestHTTPGatewayPutGet`: PUT /v1/keys/foo, GET /v1/keys/foo via real HTTP, assert round-trip
  - `TestGracefulShutdown`: start server, begin request, call Stop(), assert request completes

## 13. Open Questions

None.
