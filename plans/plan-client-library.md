# Feature: Go Client Library

## 1. Purpose

The Go client library (`pkg/client`) provides a high-level, ergonomic API for applications to interact with a GoQuorum cluster. It abstracts away gRPC transport, vector clock context threading, connection management, and basic retry logic — letting application developers use GoQuorum as if it were a simple key-value store while still getting the causal consistency guarantees when needed.

## 2. Responsibilities

- **Connection management**: Establish and maintain gRPC connections to one or more GoQuorum nodes
- **Put**: Serialize and send a Put request; return the opaque causal context for the client to thread through subsequent operations
- **Get**: Send a Get request; return the list of siblings and the causal context; handle the not-found case
- **Delete**: Send a Delete request with the causal context from a prior Get
- **Context threading**: Make it easy for callers to pass causal context across operations without managing raw bytes
- **Basic retry**: Retry transient RPC errors (e.g., connection reset, Unavailable) with exponential backoff up to a configurable limit
- **Timeout**: Apply a configurable per-operation deadline

## 3. Non-Responsibilities

- Does not implement server-side logic
- Does not implement conflict resolution (the application must resolve siblings)
- Does not implement any caching
- Does not implement client-side consistent hashing or load balancing across nodes (connects to a single endpoint or uses round-robin across seed addresses)

## 4. Architecture Design

```
Application code
    |
    v
+---------------------------+
|      client.Client        |
|                           |
|  Put(key, value) → Ctx    |
|  Get(key) → []Sibling, Ctx|
|  Delete(key, ctx)         |
|                           |
|  conn: *grpc.ClientConn   |
|  stub: GoQuorumClient     |
+---------------------------+
    |
    | gRPC
    v
GoQuorum node (gRPC :7070)
```

### Sibling Resolution

GoQuorum may return multiple siblings on `Get` when concurrent writes happened. The client library surfaces all siblings; the application is responsible for picking the "winner" and writing it back with the merged causal context:

```
sibs, ctx = client.Get("key")
if len(sibs) > 1:
    resolved = myApp.resolve(sibs)
    client.Put("key", resolved, client.WithContext(ctx))
```

## 5. Core Data Structures (Go)

```go
package client

import (
    "context"
    "time"
    "google.golang.org/grpc"
    pb "goquorum/api/proto"
)

// Client is the GoQuorum client.
type Client struct {
    conn    *grpc.ClientConn
    stub    pb.GoQuorumClient
    config  Config
}

// Config holds client tuning parameters.
type Config struct {
    // Addresses is the list of GoQuorum node gRPC addresses to connect to.
    // The client connects to the first reachable address; others are fallback.
    Addresses []string `yaml:"addresses"`
    // Timeout is the per-operation deadline.
    Timeout time.Duration `yaml:"timeout" default:"5s"`
    // MaxRetries is the maximum number of retries on transient errors.
    MaxRetries int `yaml:"max_retries" default:"3"`
    // RetryBackoff is the initial backoff duration (doubles each retry).
    RetryBackoff time.Duration `yaml:"retry_backoff" default:"100ms"`
}

// Sibling represents one version of a key's value.
type Sibling struct {
    Value []byte
    // IsTombstone is true if this sibling represents a deletion.
    IsTombstone bool
}

// Context is an opaque causal token returned by Put/Get/Delete.
// It must be passed to subsequent writes on the same key to preserve causality.
type Context []byte

// Option is a functional option for individual operations.
type Option func(*opConfig)

type opConfig struct {
    causalCtx Context
    timeout   time.Duration
}

// WithContext attaches a causal context to an operation.
func WithContext(ctx Context) Option {
    return func(c *opConfig) { c.causalCtx = ctx }
}

// WithTimeout overrides the default operation timeout.
func WithTimeout(d time.Duration) Option {
    return func(c *opConfig) { c.timeout = d }
}
```

## 6. Public Interfaces

```go
package client

// New creates a Client connected to the given addresses.
// Returns an error if no address is reachable.
func New(cfg Config) (*Client, error)

// Put stores key=value and returns the updated causal context.
// Pass WithContext(ctx) to causally order this write after a prior Get.
func (c *Client) Put(ctx context.Context, key string, value []byte, opts ...Option) (Context, error)

// Get retrieves all siblings for key along with a causal context.
// Returns ErrNotFound if the key does not exist.
// The returned []Sibling may have len > 1 if there are unresolved concurrent writes.
func (c *Client) Get(ctx context.Context, key string, opts ...Option) ([]Sibling, Context, error)

// Delete marks key as deleted using the causal context from a prior Get.
// ctx must be the Context returned by a Get call for this key.
func (c *Client) Delete(ctx context.Context, key string, causalCtx Context, opts ...Option) error

// Close releases the underlying gRPC connection.
func (c *Client) Close() error

// ErrNotFound is returned by Get when the key does not exist.
var ErrNotFound = errors.New("key not found")
```

## 7. Internal Algorithms

### Put
```
Put(ctx, key, value, opts...):
  cfg = applyOpts(c.config, opts)
  deadline, cancel = context.WithTimeout(ctx, cfg.timeout)
  defer cancel()

  req = &pb.PutRequest{
    Key:     key,
    Value:   value,
    Context: cfg.causalCtx,  // nil on first write
  }

  var resp *pb.PutResponse
  err = withRetry(c.config.MaxRetries, c.config.RetryBackoff, func():
    resp, err = c.stub.Put(deadline, req)
    return err
  )
  if err != nil: return nil, mapGRPCError(err)

  return Context(resp.Context), nil
```

### Get
```
Get(ctx, key, opts...):
  cfg = applyOpts(c.config, opts)
  deadline, cancel = context.WithTimeout(ctx, cfg.timeout)
  defer cancel()

  req = &pb.GetRequest{Key: key}

  var resp *pb.GetResponse
  err = withRetry(c.config.MaxRetries, c.config.RetryBackoff, func():
    resp, err = c.stub.Get(deadline, req)
    return err
  )
  if err != nil:
    if isNotFound(err): return nil, nil, ErrNotFound
    return nil, nil, mapGRPCError(err)

  sibs = fromProtoSiblings(resp.Siblings)
  return sibs, Context(resp.Context), nil
```

### withRetry (exponential backoff)
```
withRetry(maxRetries, initialBackoff, fn):
  backoff = initialBackoff
  for attempt in 0..maxRetries:
    err = fn()
    if err == nil: return nil
    if !isRetryable(err): return err
    if attempt < maxRetries:
      time.Sleep(backoff)
      backoff = min(backoff * 2, 10s)
  return err

isRetryable(err):
  code = status.Code(err)
  return code == codes.Unavailable || code == codes.DeadlineExceeded
```

## 8. Persistence Model

Not applicable. The client is stateless and holds no local data. The causal `Context` is an opaque byte slice managed by the caller's application.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| gRPC connection goroutine-safety | `grpc.ClientConn` and generated stubs are safe for concurrent use |
| No shared mutable state | `Client` fields are set at construction and never modified |

Multiple goroutines may call `Put`, `Get`, and `Delete` on the same `Client` instance concurrently without external locking.

## 10. Configuration

```go
type Config struct {
    Addresses    []string      `yaml:"addresses"`
    Timeout      time.Duration `yaml:"timeout" default:"5s"`
    MaxRetries   int           `yaml:"max_retries" default:"3"`
    RetryBackoff time.Duration `yaml:"retry_backoff" default:"100ms"`
    // TLSEnabled and TLSCACert are used when TLS is configured (see plan-tls-security.md).
    TLSEnabled bool   `yaml:"tls_enabled" default:"false"`
    TLSCACert  string `yaml:"tls_ca_cert"`
}
```

## 11. Observability

The client library does not expose Prometheus metrics (it runs inside the application process). Applications are expected to instrument at a higher level. However, the client:
- Returns typed errors that callers can inspect (ErrNotFound, QuorumError, etc.)
- Includes the key name and operation in error messages for easy log correlation
- Logs retry attempts at DEBUG level via an optional `*slog.Logger` passed in Config

## 12. Testing Strategy

- **Unit tests**:
  - `TestPutSuccess`: mock gRPC server returns success, assert context returned
  - `TestPutWithCausalContext`: WithContext option sets context field in request
  - `TestGetSuccess`: mock returns one sibling, assert sibling and context returned
  - `TestGetNotFound`: mock returns NotFound, assert ErrNotFound returned
  - `TestGetMultipleSiblings`: mock returns 2 siblings, assert both returned
  - `TestDeleteRequiresContext`: nil causal context, assert error
  - `TestRetryOnUnavailable`: mock fails with Unavailable then succeeds, assert success after retry
  - `TestRetryExhausted`: mock always fails with Unavailable, assert error after MaxRetries
  - `TestNoRetryOnInvalidArgument`: assert InvalidArgument not retried
  - `TestTimeoutApplied`: mock hangs, assert DeadlineExceeded returned within configured timeout
  - `TestConcurrentSafety`: 100 goroutines calling Put concurrently, assert no race (-race flag)
- **Integration tests**:
  - `TestClientPutGetDeleteE2E`: real 1-node cluster, Put → Get → Delete → Get (assert not found)
  - `TestClientSiblingResolution`: concurrent Puts produce siblings, client resolves and re-Puts

## 13. Open Questions

None.
