# Feature: Batch Operations (MultiGet, MultiPut)

## 1. Purpose

GoQuorum currently processes Get, Put, and Delete requests one key at a time. Many real-world workloads — cache warming, bulk imports, multi-record transactions — require reading or writing many keys together. Issuing individual RPCs for each key is expensive: N keys require N round trips, each with its own quorum fanout. Batch operations allow clients to submit multiple keys in a single RPC call, enabling the coordinator to pipeline fanout RPCs and amortize connection overhead. This feature is not yet implemented.

## 2. Responsibilities

- **MultiGet**: Accept a list of keys in one RPC; fan out reads for all keys in parallel; return sibling sets and causal contexts per key
- **MultiPut**: Accept a list of (key, value, optional causal context) tuples; fan out writes for all keys with independent quorum tracking; return a per-key result map
- **MultiDelete**: Accept a list of (key, causal context) tuples; fan out deletes; return per-key results
- **Partial failure handling**: Return a per-key result for each key, distinguishing success from quorum error or not-found, so callers can retry only the failed keys
- **Parallelism control**: Limit the maximum number of concurrent per-key operations within a batch to avoid overwhelming replicas
- **Proto extensions**: Add `MultiGet`, `MultiPut`, `MultiDelete` RPC methods to `goquorum.proto`

## 3. Non-Responsibilities

- Does not provide ACID transactions or atomicity across keys — each key is handled independently
- Does not implement cross-key vector clock relationships — each key has its own independent causal history
- Does not implement server-side sorting or key ordering
- Does not implement streaming (single request/response; no gRPC streaming API)

## 4. Architecture Design

```
Client MultiGet([key1, key2, key3])
    |
    v
+---------------------------+
|  Coordinator.MultiGet     |
|                           |
|  sem = make(chan, MaxPar) |
|  for each key:            |
|    sem <- struct{}{}      |
|    go Get(key) → result   |
|    <-sem                  |
|  collect all results      |
+---------------------------+
    |
    v
MultiGetResponse:
  {
    key1: {siblings, context, err: nil}
    key2: {err: NotFound}
    key3: {siblings, context, err: nil}
  }
```

## 5. Core Data Structures (Go)

```go
package cluster

import "goquorum/internal/storage"

// GetResult is the outcome for one key in a MultiGet.
type GetResult struct {
    Siblings []storage.Sibling
    Context  *vclock.VClock
    Err      error // nil, ErrNotFound, or QuorumError
}

// PutRequest is one key-value pair in a MultiPut.
type PutRequest struct {
    Key     []byte
    Value   []byte
    Context *vclock.VClock // optional causal context; nil for unconditional write
}

// PutResult is the outcome for one key in a MultiPut.
type PutResult struct {
    Context *vclock.VClock
    Err     error
}

// DeleteRequest is one key in a MultiDelete.
type DeleteRequest struct {
    Key     []byte
    Context *vclock.VClock // required causal context from prior Get
}

// BatchConfig controls batch operation parallelism.
type BatchConfig struct {
    // MaxParallelKeys limits concurrent per-key operations within one batch.
    MaxParallelKeys int `yaml:"max_parallel_keys" default:"50"`
    // MaxBatchSize limits the number of keys per batch request.
    MaxBatchSize int `yaml:"max_batch_size" default:"500"`
}
```

## 6. Public Interfaces

```go
package cluster

// MultiGet retrieves sibling sets for multiple keys concurrently.
// Returns a map from key (as string) to GetResult for each requested key.
func (c *Coordinator) MultiGet(ctx context.Context, keys [][]byte) map[string]GetResult

// MultiPut writes multiple key-value pairs concurrently.
// Returns a map from key (as string) to PutResult.
func (c *Coordinator) MultiPut(ctx context.Context, requests []PutRequest) map[string]PutResult

// MultiDelete deletes multiple keys concurrently.
// Returns a map from key (as string) to error (nil on success).
func (c *Coordinator) MultiDelete(ctx context.Context, requests []DeleteRequest) map[string]error
```

### Proto extension (goquorum.proto)
```protobuf
service GoQuorum {
  // existing methods ...
  rpc MultiGet(MultiGetRequest) returns (MultiGetResponse);
  rpc MultiPut(MultiPutRequest) returns (MultiPutResponse);
  rpc MultiDelete(MultiDeleteRequest) returns (MultiDeleteResponse);
}

message MultiGetRequest  { repeated string keys = 1; }
message MultiGetResponse { map<string, KeyGetResult> results = 1; }
message KeyGetResult     { repeated Sibling siblings = 1; bytes context = 2; string error = 3; }

message MultiPutRequest  { repeated PutEntry entries = 1; }
message PutEntry         { string key = 1; bytes value = 2; bytes context = 3; }
message MultiPutResponse { map<string, KeyPutResult> results = 1; }
message KeyPutResult     { bytes context = 1; string error = 2; }

message MultiDeleteRequest  { repeated DeleteEntry entries = 1; }
message DeleteEntry         { string key = 1; bytes context = 2; }
message MultiDeleteResponse { map<string, string> errors = 1; } // key → error string, empty = success
```

## 7. Internal Algorithms

### MultiGet
```
MultiGet(ctx, keys):
  if len(keys) > config.MaxBatchSize:
    return error(InvalidArgument, "batch too large")

  results = map[string]GetResult{}
  mu = sync.Mutex{}
  sem = make(chan struct{}, config.MaxParallelKeys)
  wg = sync.WaitGroup{}

  for each key in keys:
    wg.Add(1)
    sem <- struct{}{}
    go func(k []byte):
      defer wg.Done()
      defer func() { <-sem }()

      sibs, vc, err = c.Get(ctx, k)
      mu.Lock()
      results[string(k)] = GetResult{sibs, vc, err}
      mu.Unlock()
    (key)

  wg.Wait()
  return results
```

### MultiPut
```
MultiPut(ctx, requests):
  if len(requests) > config.MaxBatchSize:
    return error(InvalidArgument, "batch too large")

  results = map[string]PutResult{}
  mu = sync.Mutex{}
  sem = make(chan struct{}, config.MaxParallelKeys)
  wg = sync.WaitGroup{}

  for each req in requests:
    wg.Add(1)
    sem <- struct{}{}
    go func(r PutRequest):
      defer wg.Done()
      defer func() { <-sem }()

      var vc *vclock.VClock
      var err error
      if r.Context != nil:
        // Causally ordered write: use provided context
        err = c.putWithContext(ctx, r.Key, r.Value, r.Context)
        vc = r.Context  // approximate; actual updated clock from replica
      else:
        vc, err = c.Put(ctx, r.Key, r.Value)

      mu.Lock()
      results[string(r.Key)] = PutResult{vc, err}
      mu.Unlock()
    (req)

  wg.Wait()
  return results
```

## 8. Persistence Model

Batch operations have no additional persistence requirements. Each key in the batch is persisted independently via the standard `storage.Engine.Put` path. There is no batch-level write-ahead log or atomicity guarantee.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Semaphore channel (`sem`) | Limits concurrent goroutines within one batch to `MaxParallelKeys` |
| `sync.WaitGroup` | Waits for all per-key goroutines to complete before returning |
| `sync.Mutex` on results map | Protects concurrent writes to the results map |
| `context.Context` propagation | The caller's deadline applies to the entire batch; individual keys may time out if the shared deadline expires |

## 10. Configuration

```go
type BatchConfig struct {
    MaxParallelKeys int `yaml:"max_parallel_keys" default:"50"`
    MaxBatchSize    int `yaml:"max_batch_size" default:"500"`
}
```

`MaxBatchSize` guards against oversized requests consuming excessive memory. `MaxParallelKeys` prevents replica overload when processing large batches.

## 11. Observability

- `goquorum_batch_get_size` — Histogram of keys per MultiGet request
- `goquorum_batch_put_size` — Histogram of keys per MultiPut request
- `goquorum_batch_partial_failure_total` — Counter of batches with at least one key failure
- `goquorum_batch_duration_seconds{operation}` — Histogram of full batch duration
- Log at DEBUG when a batch contains partial failures, including failed key count

## 12. Testing Strategy

- **Unit tests**:
  - `TestMultiGetAllSuccess`: 5 keys, all found, assert all returned
  - `TestMultiGetPartialNotFound`: mix of found and not-found keys, assert per-key results correct
  - `TestMultiGetParallelism`: assert goroutine count does not exceed MaxParallelKeys
  - `TestMultiPutAllSuccess`: 5 keys, all written, assert per-key contexts returned
  - `TestMultiPutPartialQuorumFailure`: one key fails quorum, assert others succeed
  - `TestMultiBatchTooLarge`: len(keys) > MaxBatchSize, assert InvalidArgument error
  - `TestMultiDeleteWithContext`: valid causal contexts, assert all deleted
  - `TestMultiDeleteMissingContext`: nil context, assert per-key InvalidArgument
  - `TestContextDeadlineApplies`: short deadline, large batch, assert incomplete keys return DeadlineExceeded
- **Integration tests**:
  - `TestBatchPutGetDelete3Nodes`: 100-key batch Put, MultiGet, MultiDelete on live 3-node cluster

## 13. Open Questions

- Should MultiPut support server-side conditional writes (optimistic concurrency via context mismatch detection)?
- Should we support gRPC streaming variants for very large batches (>500 keys)?
