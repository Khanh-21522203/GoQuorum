# GoQuorum Code Review Guide

This guide walks you through the entire codebase in a logical order — from foundational primitives up to system-level features. Each section tells you **which files to open**, **what to look for**, and **what invariants to verify**.

---

## Directory Map

```
GoQuorum/
├── api/proto/            ← gRPC service definitions (source of truth for the wire protocol)
├── api/                  ← Generated protobuf code (do not edit by hand)
├── api/cluster/          ← Generated internal RPC code
├── cmd/quorum/           ← Server entry point (main.go)
├── cmd/quorumctl/        ← CLI tool (main.go)
├── config/               ← Example YAML configs
├── internal/
│   ├── common/           ← Shared types: NodeID, Node, errors
│   ├── vclock/           ← Vector clocks (causality)
│   ├── storage/          ← Pebble-backed persistence
│   ├── config/           ← Configuration structs and loaders
│   ├── cluster/          ← Distributed systems core
│   ├── server/           ← gRPC + HTTP API layer
│   ├── security/         ← TLS helpers
│   ├── backup/           ← Backup / restore
│   └── observability/    ← Structured logging
└── pkg/client/           ← Public Go client library
```

---

## Review Order

### 1. Proto Definitions — Start Here

**Files:** `api/proto/goquorum.proto`, `api/proto/internal.proto`

These define the entire surface area of the system. Read them before any code.

**goquorum.proto** — client-facing API:
- `GoQuorum` service: `Get`, `Put`, `Delete`, `BatchGet`, `BatchPut`
- `GoQuorumAdmin` service: `Health`, `ClusterInfo`, `GetMetrics`, `KeyInfo`, `TriggerCompaction`
- `Context` message: holds `repeated ContextEntry` (node_id + counter) — this is the vector clock returned to clients
- `PutOptions.ttl_seconds` — non-zero enables key expiry
- `Sibling` message: value + context + tombstone flag + timestamp

**internal.proto** — node-to-node protocol:
- `GoQuorumInternal` service: `Replicate`, `Read`, `Heartbeat`, `AntiEntropyExchange`, `GetMerkleRoot`
- `SiblingData` carries the full sibling (including context entries) for replication

**Things to verify:**
- Does every RPC in the proto have a corresponding handler in `grpc_adapters.go`?
- Does `PutRequest.options.ttl_seconds` flow all the way to storage?

---

### 2. Vector Clocks — Foundation of Consistency

**Files:** `internal/vclock/vclock.go`, `internal/vclock/compare.go`

Vector clocks track causality between writes. Every stored value carries one.

**vclock.go — check:**
- `Tick(nodeID)` increments this node's counter. Called on every write.
- `Merge(other)` takes the element-wise max. Called during read repair and anti-entropy.
- `Copy()` deep-copies so callers don't share state.
- `MarshalBinary` / `UnmarshalBinary` — used by storage encoding. Verify format is stable.
- `Prune(threshold, maxEntries)` — drops entries with counter < threshold. Prevents unbounded growth; check thresholds in `config/storage.go`.

**compare.go — check:**
- `HappensBefore(other)` — every counter in `self` ≤ `other`, and at least one is strictly less.
- `IsConcurrentWith(other)` — neither dominates the other. This is what causes siblings.
- `Equals(other)` — all counters match exactly.
- `Dominates(other)` — self is strictly after other on every node. Used for deduplication.

**Invariant:** If `a.HappensBefore(b)` then `a` should be discarded when both are present.

---

### 3. Storage Layer — Persistence Primitives

**Files:** `internal/storage/encoding.go`, `internal/storage/engine.go`, `internal/storage/options.go`

#### 3a. Binary Encoding (`encoding.go`)

The on-disk format for a sibling set:
```
[4 bytes magic] [4 bytes CRC32] [4 bytes count]
  for each sibling:
    [2 bytes value_len] [N bytes value]
    [1 byte flags]       ← FlagTombstone=0x01, FlagTTL=0x04
    [8 bytes timestamp]
    if FlagTTL: [8 bytes expires_at]
    [vclock binary]
```

**Things to check:**
- `FlagTombstone` and `FlagTTL` are bit flags — they can be combined.
- If `FlagTTL` is not set, no `expires_at` field is written (backward compatible).
- CRC32 covers the payload; a mismatch returns a corruption error.
- `ValidateKey` / `ValidateValue` enforce 64KB / 1MB limits.

#### 3b. Storage Engine (`engine.go`)

Key types:
- `Sibling` — one version of a value: `Value`, `VClock`, `Timestamp`, `Tombstone`, `ExpiresAt`
- `SiblingSet` — the set of concurrent siblings for a key

Key methods:
- `Put(key, siblingSet)` — merges incoming siblings with existing ones using vector clock dominance; discards dominated siblings.
- `Get(key)` — returns non-tombstone siblings (filtered). Used by the client-facing layer.
- `GetRaw(key)` — returns all siblings including tombstones; also applies lazy TTL expiry. Used by the coordinator for read repair.
- `Scan(prefix, limit)` — used by TTL sweeper and anti-entropy.
- `DB()` — exposes `*pebble.DB` for checkpoint-based backup.

**Things to check:**
- Does `Put` correctly merge: keep all non-dominated siblings, plus incoming ones that aren't dominated?
- Does `GetRaw` filter expired siblings (ExpiresAt != 0 && now >= ExpiresAt) before returning?
- Does `Get` return `ErrKeyNotFound` when all siblings are tombstones or expired?
- Background `runTombstoneGC` — does it copy `iter.Key()` before calling `db.Delete`? (Iterator key becomes invalid after mutation.)
- Sync writes: is `pebble.Sync` vs `pebble.NoSync` correctly chosen based on `opts.SyncWrites`?

---

### 4. Configuration — Wiring All the Knobs

**Files:** `internal/config/config.go`, `internal/config/cluster.go`, `internal/config/quorum.go`, `internal/config/repair.go`, `internal/config/tls.go`, `internal/config/connection.go`, `internal/config/storage.go`, `internal/config/failure_detector.go`

**config.go** — top-level `Config` struct. Check `applyDefaults()` fills in zeros, and `Validate()` enforces bounds.

**quorum.go — critical parameters:**
- `N` = replication factor (number of replicas per key)
- `R` = read quorum (minimum successful reads for a consistent read)
- `W` = write quorum (minimum successful writes for a durable write)
- `SloppyQuorum bool` — if true, overflow nodes can substitute failed primary nodes
- Strong consistency requires `R + W > N`
- `ToleratedFailures()` = `N - max(R, W)`

**repair.go:**
- `ReadRepairConfig.Probability` — fraction of reads that trigger async repair (0.0–1.0)
- `AntiEntropyConfig.Interval` — how often anti-entropy rounds run

**Things to check:**
- Is every config field in `Config` actually consumed somewhere in `main.go`?
- Does `Gossip.FanOut` default to 3 and `Gossip.Interval` to 1s?
- Does `RateLimit.GlobalRPS = 0` correctly disable rate limiting?

---

### 5. Hash Ring — Key Distribution

**File:** `internal/cluster/hashring.go`

Consistent hashing with virtual nodes (default 256 per physical node).

**Key methods:**
- `AddNode(node)` — adds physical node + 256 virtual nodes; re-sorts the ring. Hash: `xxhash("nodeID:vnodeIndex")`.
- `RemoveNode(nodeID)` — removes all vnodes for that node. Does **not** re-sort (already sorted; just filters).
- `GetPreferenceList(key, N)` — hashes key, binary searches for the first vnode ≥ hash, then walks clockwise collecting N distinct physical nodes.
- `GetExtendedPreferenceList(key, N, extra)` — same but collects up to N+extra nodes (used by sloppy quorum).
- `GetPrimaryNode(key)` — convenience for N=1.

**Things to check:**
- Does `GetPreferenceList` wrap around when the key hash is beyond the last vnode?
- Are duplicate physical nodes skipped when walking clockwise?
- `GetExtendedPreferenceList`: are overflow nodes (indices N onwards) actually not in the strict preference list?

---

### 6. Coordinator — The Write/Read Brain

**File:** `internal/cluster/coordinator.go`

This is the most important file. Every client operation flows through here.

#### Put flow:
1. Validate key and value.
2. Tick vector clock: `newVClock = context.Copy(); newVClock.Tick(nodeID)`.
3. Build `SiblingSet` with optional `ExpiresAt` if `TTLSeconds > 0`.
4. Get preference list: `ring.GetPreferenceList(key, N)`.
5. `parallelWrite` — fan out to all N replicas concurrently.
6. Count successes.
7. If `successCount >= W` → success.
8. If `SloppyQuorum` and `successCount < W`: get extended list, write to overflow nodes.
9. If still `< W` → return `QuorumError`.

#### Get flow:
1. Get preference list.
2. `parallelRead` — early return after R successful reads.
3. `mergeAllSiblings` — keep only causally maximal siblings.
4. If `successCount < R` → `QuorumError`.
5. Trigger read repair (async or sync).
6. Filter tombstones. If empty → `ErrKeyNotFound`.

#### parallelWrite:
- Uses `sync.WaitGroup` — all goroutines complete before returning (prevents goroutine leaks after storage close).
- Local write fast path: if `nodeID == c.nodeID`, call `localWrite()` directly (no RPC).
- After all writes: if `hintedHandoff != nil`, store hints for every failed remote write.

**Things to check:**
- Does sloppy quorum identify overflow nodes correctly (nodes in extended list but not in strict prefix)?
- Does `mergeAllSiblings` correctly discard dominated siblings while preserving concurrent ones?
- Does `filterTombstones` happen after merge (so tombstones can suppress old non-tombstone siblings)?
- Does `Delete` require a non-empty context (vector clock)? What happens without it?
- Does `BatchGet` / `BatchPut` share the same per-key concurrency as single operations?

---

### 7. Membership and Failure Detection

**Files:** `internal/cluster/membership.go`, `internal/cluster/failure_detector.go`

**membership.go:**
- Tracks each peer's status: `NodeStatusActive`, `NodeStatusSuspect`, `NodeStatusFailed`, `NodeStatusLeaving`.
- `UpdatePeerStatus(nodeID, status)` — state transitions; called by failure detector and gossip.
- `HasQuorum()` — checks if enough nodes are active to satisfy quorum.
- `GetPeers()` — returns all known peers; used by gossip, failure detector, and graceful shutdown.

**failure_detector.go:**
- Sends periodic heartbeats to all peers via `rpc.SendHeartbeat()`.
- On `FailThreshold` consecutive missed heartbeats: marks node as Suspect, then Failed.
- Calls `membership.UpdatePeerStatus(nodeID, NodeStatusFailed)`.

**Things to check:**
- Does `FailureDetector.Start()` get called in `main.go`? (It should, via `cluster.Bootstrap`.)
- Does `Bootstrap` wait for a quorum of members to be active before returning?
- Is `NodeStatusLeaving` set on shutdown via `NotifyLeaving` RPC?

---

### 8. Anti-Entropy — Background Replica Sync

**Files:** `internal/cluster/anti_entropy.go`, `internal/cluster/merkle_tree.go`

#### Merkle Tree (`merkle_tree.go`):
- Leaf count = `1 << depth` buckets. Each key hashes to one bucket.
- `UpdateKey(key, siblingSet)` — XOR-toggle the key's hash into its bucket leaf. Called on every write.
- `RemoveKey(key, oldSiblingSet)` — XOR-toggle out the old hash (XOR is its own inverse).
- `GetRoot()` — SHA256 of all internal nodes, built lazily when dirty.
- `Compare(other)` — returns list of `BucketRange` where the two trees differ.

**Invariant:** XOR toggling is symmetric — toggling the same key twice returns the leaf to its original state.

**anti_entropy.go:**
- `OnKeyUpdate(key, siblingSet)` — updates Merkle tree incrementally (faster than full rebuild).
- Background loop: picks a random peer, calls `rpc.GetMerkleRoot()`, compares with local root.
- If roots differ, calls `Compare()` to find differing bucket ranges, then fetches and pushes keys in those ranges.

**Things to check:**
- Does `NewMerkleTree` initialize all leaf hashes to zero-slices (not nil)? Nil slices cause XOR panics.
- Does `Compare` use `rebuildIfNeeded()` (not `GetRoot()`) to avoid deadlock on `mt.mu`?
- Does `AntiEntropy.Start()` get called via `coordinator.Start()`?

---

### 9. Read Repair

**File:** `internal/cluster/read_repair.go`

Triggered during `Get` when some replicas returned stale data.

- `TriggerRepair(ctx, key, merged, responses)` — compares each replica's response against the merged result. If a replica is missing a sibling or has an older version, it pushes the merged result to that replica.
- Probabilistic: skipped when `rand.Float64() > config.Probability`.
- Async: runs in a goroutine when `config.Async == true`.

**Things to check:**
- Does repair send the full merged sibling set (not just the missing siblings)?
- Does it skip repair for replicas that returned an error (vs. returned stale data)?
- Is the repair timeout (`RepairTimeout`) correctly applied?

---

### 10. Hinted Handoff

**File:** `internal/cluster/hinted_handoff.go`

Durability mechanism: when a write to a primary replica fails, the coordinator stores a hint and replays it when the target recovers.

- `StoreHint(targetNode, key, siblingSet)` — appends to in-memory per-node hint list. Capped at `maxHintsPerNode` (1000); oldest are dropped.
- `replayLoop()` — ticks every 30s; calls `replayAll()`.
- `replayHintsForNode(nodeID)` — only runs if target is `NodeStatusActive`. Calls `rpc.RemotePut` for each hint. Drops hints older than 24h. Retains failed hints for retry.

**Wiring check in `main.go`:**
```go
hintedHandoff := cluster.NewHintedHandoff(membership, rpcClient, cfg.Node.NodeID)
coordinator.SetHintedHandoff(hintedHandoff)
// HintedHandoff.Start() is called inside coordinator.Start()
```

**Things to check:**
- Is `Start()` called before writes arrive (i.e., before `coordinator.Start()`... actually inside it)?
- Are hints stored only for remote writes (not local writes where `nodeID == c.nodeID`)?
- Does `replayHintsForNode` correctly skip nodes that aren't Active?

---

### 11. TTL Sweeper

**File:** `internal/cluster/ttl_sweeper.go`

Background process to clean up expired keys.

- `sweep()` — scans all keys via `storage.Scan`, checks `Sibling.ExpiresAt`, issues `coordinator.Delete` for expired keys.
- Rate-limited: stops after 1000 deletes per sweep to avoid write amplification.
- Default interval: configurable (passed to `NewTTLSweeper`); 0 means use internal default.

**Wiring check in `main.go`:**
```go
ttlSweeper := cluster.NewTTLSweeper(store, coordinator, 0)
ttlSweeper.Start()
defer ttlSweeper.Stop()
```

**Things to check:**
- Does `sweep` build a causally-correct vector clock for the delete? (Needs to merge all sibling VClocks so the tombstone dominates them.)
- Is TTL expiry also checked lazily in `storage.GetRaw`? (Defense-in-depth: sweeper for cleanup, GetRaw for immediate expiry.)

---

### 12. Sloppy Quorum

**File:** `internal/cluster/coordinator.go` (inside `Put`) + `internal/cluster/hashring.go`

When strict quorum cannot be reached, overflow nodes from outside the strict preference list temporarily hold the data.

**Flow in `Put`:**
1. Standard `parallelWrite` to `prefList[0..N-1]`.
2. If `successCount < W` and `cfg.SloppyQuorum == true`:
   - Call `ring.GetExtendedPreferenceList(key, N, need)` — returns N strict + `need` overflow.
   - Identify overflow nodes: nodes in the extended list beyond index N that aren't in `strictSet`.
   - `parallelWrite` to overflow nodes.
3. Hints are stored for the original failed primary nodes (by `parallelWrite`'s hint-storing logic).

**Things to check:**
- Is `need = W - successCount` the right number of overflow nodes to request?
- Are overflow nodes correctly excluded from `strictSet`?
- Does the sloppy-quorum success path also notify anti-entropy (`OnKeyUpdate`)?

---

### 13. Gossip Membership Protocol

**File:** `internal/cluster/gossip.go`

Propagates node state changes across the cluster using epidemic broadcast.

- `NodeEntry` — per-node state snapshot: `NodeID`, `Addr`, `Status`, `Version` (logical LWW counter), `UpdatedAt`.
- `spreadLoop()` — every `Interval`, picks `FanOut` random peers, calls `POST /internal/gossip/exchange`.
- `exchange(peer)` — sends `GetState()` (JSON map), receives peer's state, calls `Merge`.
- `Merge(incoming)` — Last-Write-Wins per node by `Version`. If version is higher: update local state + call `membership.UpdatePeerStatus`.
- `SetSelf(status)` — increments own Version, updates own entry; ensures local changes propagate.

**Wiring check in `main.go` and `server.go`:**
```go
gossip := cluster.NewGossip(nodeID, httpAddr, membership, gossipCfg)
gossip.Start()
srv.SetGossip(gossip)          // attaches to internalAPI
// Route: POST /internal/gossip/exchange → internalAPI.handleGossipExchange
```

**Things to check:**
- Is `Merge` safe to call concurrently (mutex-protected)?
- Does `handleGossipExchange` correctly decode the incoming state, merge, and respond with local state?
- Is `SetSelf` called when this node's own status changes (e.g., on `NotifyLeaving`)?

---

### 14. Graceful Shutdown

**File:** `internal/cluster/graceful_shutdown.go`

**Steps in `Shutdown(ctx)`:**
1. Stop accepting new requests (stop `FailureDetector`).
2. Drain in-flight requests: poll `coordinator.InFlightCount()` until zero.
3. Notify peers via `rpc.NotifyLeaving(ctx, nodeID)` (best-effort; log failures, don't block).
4. Stop server, stop coordinator, close storage.

**Things to check:**
- Is there a drain timeout? What happens if in-flight requests never reach zero?
- Does `NotifyLeaving` call `membership.UpdatePeerStatus(nodeID, NodeStatusLeaving)` on the receiving node?
- Is the HTTP handler for `/internal/notify-leaving` registered in `server.go`?

---

### 15. Server Layer — API Wiring

**Files:** `internal/server/server.go`, `internal/server/grpc_adapters.go`, `internal/server/client_api.go`, `internal/server/internal_api.go`, `internal/server/admin_api.go`

#### server.go — route registration:
All HTTP routes and gRPC services are registered in `Start()` → `startHTTP()`. Verify each route has a handler:

| Route | Handler |
|-------|---------|
| `POST /internal/replicate` | `handleInternalReplicate` |
| `POST /internal/read` | `handleInternalRead` |
| `POST /internal/heartbeat` | `handleInternalHeartbeat` |
| `POST /internal/merkle-root` | `handleInternalMerkleRoot` |
| `POST /internal/notify-leaving` | `handleInternalNotifyLeaving` |
| `POST /internal/gossip/exchange` | `internalAPI.handleGossipExchange` (gated) |
| `POST /admin/backup` | `handleAdminBackup` |
| `POST /admin/restore` | `handleAdminRestore` |
| `/health`, `/health/live`, `/health/ready` | health handlers |
| `/metrics` | Prometheus |
| `/debug/cluster`, `/debug/ring` | debug handlers |
| `/v1/*` | grpc-gateway (client + admin API) |

**grpc_adapters.go — verify every proto RPC is implemented:**
- `goQuorumGRPCServer`: Get, Put, Delete, BatchGet, BatchPut
- `goQuorumAdminGRPCServer`: Health, ClusterInfo, GetMetrics, KeyInfo, TriggerCompaction
- `goQuorumInternalGRPCServer`: Replicate, Read, Heartbeat, AntiEntropyExchange, GetMerkleRoot

**client_api.go — key checks:**
- `Get`: filters tombstones before returning to client.
- `Put`: passes `ttlSeconds` to `coordinator.Put` via `PutOptions`.
- `Delete`: requires non-nil, non-empty `VClockContext`.
- `BatchGet` / `BatchPut`: concurrent via goroutines.

---

### 16. Rate Limiting

**File:** `internal/server/rate_limiter.go`

Token bucket limiter registered as a gRPC unary interceptor.

- Two limiters: `global *rate.Limiter` and `perIP sync.Map` of `*rate.Limiter`.
- Burst = `GlobalRPS * BurstFactor` (default BurstFactor = 1.0).
- Peer IP extracted via `peer.FromContext(ctx).Addr`.
- Returns `codes.ResourceExhausted` when either limit is exceeded.

**Wiring check in `server.go`:**
```go
if s.config.RateLimit.GlobalRPS > 0 {
    rl := NewRateLimiter(s.config.RateLimit)
    grpcOpts = append(grpcOpts, grpc.UnaryInterceptor(rl.UnaryInterceptor()))
}
```

**Things to check:**
- Is `BurstFactor` applied to per-IP limiter as well?
- Does `perIP sync.Map` cause unbounded memory growth for many unique IPs? (No eviction currently.)

---

### 17. TLS / mTLS

**Files:** `internal/security/tls.go`, `internal/config/tls.go`

- `LoadServerTLSConfig(cfg)` — loads cert+key; if `MTLSEnabled`, loads CA pool and sets `ClientAuth: tls.RequireAndVerifyClientCert`.
- `LoadClientTLSConfig(cfg)` — loads CA pool; if `MTLSEnabled`, also loads client cert+key.

**Wiring check in `server.go`:**
```go
if s.config.TLS.Enabled {
    tlsCfg, _ := security.LoadServerTLSConfig(s.config.TLS)
    grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
}
```

**Wiring check in `rpc_client.go`:**
```go
if cfg.TLS.Enabled {
    tlsCfg, _ := security.LoadClientTLSConfig(cfg.TLS)
    transport.TLSClientConfig = tlsCfg
}
```

**Note:** TLS only applies to gRPC (client-facing). Internal HTTP node-to-node RPC also uses the TLS transport when enabled.

---

### 18. Backup and Restore

**Files:** `internal/backup/backup.go`, `internal/server/backup_api.go`

#### backup.go:
- `Backup(ctx, db, cfg)`:
  1. `db.Checkpoint(tmpDir, pebble.WithFlushedWAL())` — consistent snapshot.
  2. Tar + gzip checkpoint dir → `cfg.DestDir/<timestamp>.tar.gz`.
  3. Compute SHA256 of archive.
  4. Write `<timestamp>-manifest.json` alongside archive.
  5. Remove temp checkpoint dir.
- `Restore(ctx, cfg, archiveFile, destDataDir)`:
  1. Read manifest, verify SHA256 before extracting.
  2. Extract tar.gz to `destDataDir` with path sanitization (no `../` traversal).

**HTTP endpoints:**
- `POST /admin/backup` — body: `{"dest_dir": "/path/to/backup"}` → returns `{"archive": "/path/to/backup/...tar.gz"}`
- `POST /admin/restore` — body: `{"archive_file": "...", "dest_data_dir": "..."}` → returns `{"status": "ok"}`

**Things to check:**
- Does `Restore` verify the checksum *before* writing any files to `destDataDir`?
- Does tar extraction reject paths with `..` components?
- Is `ctx` cancellation checked during the tar loop?

---

### 19. Client Library

**File:** `pkg/client/client.go`

Public Go library for application developers.

- `NewClient(cfg)` — dials gRPC with configurable timeout.
- `Get(ctx, key)` — returns `[]Sibling`; empty slice means key not found.
- `Put(ctx, key, value, context)` — returns new `*vclock.VectorClock` to use in the next write.
- `Delete(ctx, key, context)` — requires the vector clock from the last Get/Put.
- `BatchGet` / `BatchPut` — multi-key operations.
- `withRetry(fn)` — retries on `codes.Unavailable` and `codes.DeadlineExceeded` with exponential backoff + jitter. **Does not retry** `codes.NotFound`, `codes.InvalidArgument`, etc.

**Things to check:**
- Does `withRetry` guard against `rand.Int63n(0)` panic when `jitterRange == 0`?
- Does `Put` return the new context so callers can pass it to the next `Put` or `Delete`?
- Is `LWWResolver` (Last-Write-Wins by timestamp) provided for clients that don't want to handle siblings?

---

### 20. CLI Tool

**File:** `cmd/quorumctl/main.go`

Commands and their underlying calls:

| Command | Uses |
|---------|------|
| `get <key>` | `client.Get` → print siblings + base64 context |
| `put <key> <value>` | `client.Put` → print returned context |
| `delete <key> <ctx>` | decode base64 context → `client.Delete` |
| `status` | `AdminClient.Health` |
| `ring` | `AdminClient.ClusterInfo` |
| `key-info <key>` | `AdminClient.KeyInfo` |
| `compact` | `AdminClient.TriggerCompaction` |

**Things to check:**
- Does `delete` correctly decode the base64 JSON context printed by `put` or `get`?
- Is the `--addr` flag used for both the data client and the admin client (same gRPC port)?

---

### 21. Entry Point — Startup Sequence

**File:** `cmd/quorum/main.go`

Verify the exact startup order:

```
1.  LoadConfig
2.  NewStorage
3.  NewMembershipManager
4.  NewHashRing + AddNode for each member
5.  NewGRPCClient (RPC client)
6.  NewCoordinator
7.  NewFailureDetector
8.  NewGossip → gossip.Start()
9.  NewHintedHandoff → coordinator.SetHintedHandoff()
10. NewTTLSweeper → ttlSweeper.Start()
11. NewServer → srv.SetGossip(gossip) → srv.Start()
12. coordinator.Start()  ← starts anti-entropy + hinted handoff
13. Bootstrap (join cluster, wait for quorum)
14. NewGracefulShutdown
15. Wait for SIGTERM/SIGINT
16. srv.Stop()
17. coordinator.Stop()  ← stops anti-entropy + hinted handoff
18. ttlSweeper.Stop()
19. gossip.Stop()
20. shutdown.Shutdown(ctx)
```

**Things to check:**
- Is `coordinator.Start()` called *after* `srv.Start()`? (Server must be up before anti-entropy starts sending RPCs to peers.)
- Is `ttlSweeper.Stop()` called via `defer` before or after `coordinator.Stop()`? (Sweeper calls `coordinator.Delete`, so coordinator should stop first.)
- Does `defer store.Close()` run *after* all goroutines that use storage have stopped?

---

## Cross-Cutting Concerns

### Context propagation
Every operation takes a `context.Context`. Timeouts are applied at the coordinator level (`timeoutConfig.ReplicaTimeout` per replica call). Client can also set an outer timeout via the gRPC deadline.

### Error handling
- `common.QuorumError` is returned when quorum is not reached. It carries `Required`, `Achieved`, and per-replica errors.
- `convertError` in `client_api.go` maps internal errors to gRPC status codes. Check the mapping is complete.
- Storage returns `common.ErrKeyNotFound` (not nil) for missing keys.

### Concurrency
- `storage.Put` uses per-key locks (`sync.Map` of `*sync.Mutex`) to prevent concurrent sibling divergence on the same node.
- `HashRing` uses `sync.RWMutex` (read lock for lookups, write lock for add/remove).
- `MembershipManager` is mutex-protected.
- `parallelWrite` uses `sync.WaitGroup` — goroutines always complete before the function returns.

### Metrics
Every subsystem registers Prometheus counters/histograms. Check `observability/` and `*_metrics.go` files. Metrics endpoint: `GET /metrics`.

---

## Quick Checklist

Before marking a feature reviewed, verify:

- [ ] Feature has an entry in the proto (if client-facing) or an HTTP route (if internal)
- [ ] Config fields are parsed in `config/` and applied in `main.go` or the relevant component
- [ ] Background goroutines have a `Stop()` that is deferred in `main.go`
- [ ] Error paths return appropriate gRPC status codes (not raw Go errors)
- [ ] Concurrent writes use per-key locking or are otherwise safe
- [ ] `go build ./...` and `go test ./...` both pass after any change
