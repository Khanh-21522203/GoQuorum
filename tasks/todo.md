# GoQuorum v2 — Task Log

This file is a running log of major work passes on `v2/`. Earlier entries are
summarized rather than reproduced verbatim where the underlying detail is now
superseded by later work.

## Phase 1 — Scaffold (superseded by Phase 2 below)

The original pass built the `v2/` tree from scratch: 8 Go modules
(`contracts -> engine -> {infra, gateway, client} -> server -> {cli, test}`),
`go.work`, per-module `go.mod`, `CONVENTIONS.md`/`INTERFACES.md`, and 102 `.go`
files of typed stubs (every method returning `contracts.ErrNotImplemented`,
carrying a `// TODO(v2): ...` comment pointing at the v1 reference
implementation at repo root). Verified at the time: `go build`/`go vet`/`go
test` clean across all 8 modules, zero external dependencies, `engine` pure
(stdlib + `contracts` only), both port interfaces (`storage.Storage`,
`transport.Transport`) conformance-checked via `var _ ... = (*Concrete)(nil)`.

Key scaffold-phase decisions that still hold: `Coordinator`'s spec surface is
`Start/Stop/Put/Get/Delete/GetMerkleRoot` only (v1's `BatchGet/BatchPut`,
`JoinNode/LeaveNode`, `SetHintedHandoff`, `InFlightCount` are out of scope);
hash-ring virtual-node count hardcoded to 256; `engine/config` types carry no
yaml tags (loading is an infra concern).

## Phase 2 — Single-threaded reactor + state machine + io_uring

### Context

The scaffold's TODO comments described a goroutine-per-subsystem,
mutex-guarded concurrency model (one `time.Ticker` goroutine per background
subsystem, goroutine-per-replica fan-out in the coordinator), mirroring v1.
This pass replaces that entirely with a single-threaded event-loop/reactor
driving explicit table-driven state machines, plus a real io_uring-backed
transport (SBE-style binary wire format) and a real io_uring-native embedded
storage engine — so every piece of mutable engine state is touched from
exactly one goroutine and needs no locks. Full design rationale lives in the
plan this pass executed: `~/.claude/plans/cheeky-bouncing-hickey.md`.

### What shipped, real and tested (no `ErrNotImplemented`)

- `engine/reactor` — single-threaded run loop: `EventSource` port, a
  `container/heap` timer wheel (`ScheduleOnce`/`ScheduleEvery`/`CancelTimer`),
  a `PostFunc` task queue as the one legitimate cross-goroutine ingress point.
- `engine/statemachine` — generic `Machine[S,T]`: table-driven, exhaustive
  `(state, trigger) -> state` dispatch, rejects any undeclared transition.
- `contracts/vclock` — full vector-clock implementation: Tick/Set/Merge/Copy,
  `Compare` (Before/After/Equal/Concurrent) via union-of-entries, Prune,
  binary + JSON marshaling.
- `engine/hashring`, `engine/membership`, `engine/antientropy/merkletree.go`
  (SHA-256-based) — implemented for real, mutexes removed (single-reactor-
  thread invariant replaces locking).
- All five engine subsystems ported onto reactor + state machine, mutexes/
  `stopCh`/`sync.WaitGroup` removed from every one:
  - `engine/gossip` — lifecycle state machine, round timer, peer exchange via
    the new `transport.GossipExchange` port method.
  - `engine/failuredetector` — lifecycle machine + one per-peer
    `Machine[node.NodeState, peerTrigger]`; `OnNodeFailed`/`OnNodeRecovery`
    each fire exactly once, on the crossing edge only.
  - `engine/handoff` — lifecycle machine, replay timer, requeue-on-failure.
  - `engine/antientropy` — lifecycle machine (idle/building/running/stopped);
    `SyncWithPeers` made callback-based (was synchronous/blocking in v1,
    incompatible with one thread).
  - `engine/coordinator` — lifecycle machine + one per-request machine per
    in-flight Put/Get/Delete; `Put`/`Get`/`Delete` made callback-based (no
    `context.Context`); quorum success fires `done` as soon as W/R is
    reached without waiting on stragglers; Get merges concurrent siblings by
    vector-clock dominance and triggers read-repair on stale replicas.
    Sloppy-quorum overflow is explicitly deferred (`// TODO(v2)` at the
    relevant branch).
  - `engine/readrepair.TriggerRepair` implemented for real alongside the
    coordinator work (was still an empty stub with the old `ctx` signature).
- `engine/storage.Storage` and `engine/transport.Transport` ports: every I/O
  method converted from blocking/`context.Context` to callback-based
  (`done func(...)`); `Transport` gained `GossipExchange`.
  `storage.SiblingSet`/`Sibling` gained real `MarshalBinary`/`UnmarshalBinary`
  (shared by the journal storage engine and the iouring wire format).
- `infra/ioruntime` — real `engine/reactor.EventSource` backed by a real
  io_uring instance (`github.com/iceber/iouring-go`). Proven end-to-end on
  this host: a real file pwrite+pread round trip, and a real TCP
  accept/send/recv loopback, both driven through an actual `reactor.Reactor`.
- `infra/transport/iouring` — hand-rolled SBE-style binary wire format
  (`wire.go`, `frame.go`: 16-byte frame header, length-prefixed messages,
  `Reassembler` for partial/coalesced TCP reads) for all 6 `Transport` RPCs,
  fully round-tripped and fuzz-tested (~20M fuzz executions, zero crashes).
  `conn.go`/`client.go`/`server.go` wire `Heartbeat` real end-to-end over a
  real io_uring TCP connection; the other 5 RPCs have real wire encoding but
  stubbed network glue (`// TODO(v2): wire into conn.go following
  Heartbeat's pattern` — the pattern is proven once, mechanically identical
  for the rest).
- `infra/storage/journal` (new package) — an io_uring-native append-only WAL
  + in-memory index implementing `engine/storage.Storage` for real: CRC32'd
  records, synchronous crash-replay with truncate-on-corruption, real
  Put/Get/Delete/Scan/Close over real io_uring reads/writes. No
  compaction/segment rotation in this pass (explicit non-goal).
- `server/app/server.go` + `cli/cmd/quorum/main.go` — composition root now
  builds one `ioruntime.Runtime` + `reactor.Reactor` driving the journal
  storage engine and the iouring transport (client dials every configured
  peer, server listens on the local node's configured internal-RPC address).
  `Server.Start()` (non-blocking) / `Server.Run()` (blocks — the process's
  actual event loop) / `Server.RequestStop()` (signal-safe) / `Server.Stop()`
  (final cleanup, only after `Run` returns) replaces the old single
  `Start`/`Stop` pair built around `go httpServer.Serve(ln)`.

### Left untouched, still typed stubs (deliberate, per the agreed cut line)

- `infra/storage/pebble`, `infra/transport/httprpc` — preserved as
  non-reactor-based fallback adapters, not wired into `server/app` anymore
  (the composition root always uses the real `journal`/`iouring` path now).
- `gateway/http`, `server/api`, `client` module — unchanged; none of them
  call into the coordinator's now-callback-based methods yet (all still
  TODO-commented stub bodies), so none needed updating for the signature
  change.
- `config.Load` — still `ErrNotImplemented` (no yaml wiring yet), so the
  built `quorum`/`quorumctl` binaries compile and pass their tests but
  cannot actually load a real config file or run end-to-end yet.
- Gossip/failure-detector/hinted-handoff are still not wired into
  `server/app` (same scope note as the original scaffold: out of
  `engine/coordinator`'s spec surface, left for a later phase).

### New external dependency

`github.com/iceber/iouring-go` (`infra/go.mod`, `require` only — no other
new deps). **Forked locally** into `v2/vendor/iouring-go` (MIT license,
retained) because the only published version (last released April 2023)
uses an unauthorized `//go:linkname` pull into two unexported `syscall`
internals (`syscall.Sockaddr.sockaddr`, `syscall.anyToSockaddr`) that Go
1.25's linker rejects outright — a hard incompatibility, not a config
issue. The fork's only change is `utils.go`: both functions reimplemented
against public `syscall` types (`RawSockaddrInet4/6/Unix`), covering
AF_INET/AF_INET6/AF_UNIX. `infra/go.mod` has a `replace
github.com/iceber/iouring-go => ../vendor/iouring-go` directive. This is a
plain `replace`, not Go's native `vendor/` mechanism (no `modules.txt`, and
it lives at the `v2/` workspace root rather than inside `infra/`'s own
module root), so it does not trigger `-mod=vendor` auto-detection — the
name was chosen only to signal "external, checked-in code" consistently
with the rest of this file's terminology.
Every file touching io_uring carries `//go:build linux` with a `!linux`
stub counterpart returning `contracts.ErrNotImplemented`, so
`GOOS=darwin GOARCH=arm64 go build goquorum.io/v2/...` stays green.

### Verification (from `v2/`, this host: Linux 6.17, io_uring-capable)

```
go build goquorum.io/v2/...                          -> clean
go vet goquorum.io/v2/...                             -> clean
go test goquorum.io/v2/... -race -count=1             -> all packages pass
GOOS=darwin GOARCH=arm64 go build goquorum.io/v2/...  -> clean
go build ./cli/cmd/quorum, ./cli/cmd/quorumctl        -> both binaries compile
gofmt -l .                                            -> clean (incl. the
                                                          one vendor file
                                                          we authored)
```

### Attention / follow-ups for whoever picks this up next

- Wire the remaining 5 `Transport` RPCs (`RemotePut`, `RemoteGet`,
  `GetMerkleRoot`, `NotifyLeaving`, `GossipExchange`) onto `conn.go`,
  following `Heartbeat`'s exact pattern in `client.go`/`server.go`.
- Sloppy-quorum overflow (`config.QuorumConfig.SloppyQuorum`) is unhandled
  in `coordinator.Put`/`Delete` — marked with a `// TODO(v2)` at the branch
  that needs it.
- `config.Load` needs real yaml wiring before the built binaries can
  actually run against a config file.
- Gossip/failure-detector/hinted-handoff still need wiring into
  `server/app.New`/`Start`/`Stop` if this deployment wants dynamic
  membership rather than the static `cfg.Cluster.Members` list.
- `infra/storage/journal` has no compaction/segment rotation — the WAL
  file grows unboundedly; fine for this pass, not for a real deployment.
- The `iouring.Client`/`Server` userData-encoding collision-avoidance
  between `Client`/`Server` (fd-range-based) and `journal.Store` (small
  sequential counter) is documented as "pragmatic, not proven" in
  `infra/transport/iouring/doc.go` — worth a real proof or a redesign
  (e.g. a shared userData allocator) if this ships beyond a prototype.
- Native io_uring HTTP gateway: explore replacing `net/http` with an
  io_uring-driven HTTP/1.1 parser directly on the reactor loop to achieve a
  100% single-goroutine binary (eliminating goroutine-per-connection and
  `PostFunc` channel hops).

## Phase 3 — Reactor CPU core pinning

`infra/affinity` (new package): `LockToCore(core int) error` locks the
calling goroutine to its OS thread (`runtime.LockOSThread`) and pins that
thread's affinity to one CPU core via `golang.org/x/sys/unix.SchedSetaffinity`
(promoted from an indirect to a direct dependency of `infra` — it was already
pulled in transitively by `iceber/iouring-go`). `!linux` stub included, same
pattern as every other io_uring-adjacent file. `infra/config.NodeConfig`
gained `ReactorCPUCore *int` (nil = no pinning, the default).
`server/app.Server.Run` pins to that core, if set, before entering
`s.reactor.Run()` — the natural spot, since `Run`'s own doc comment already
says it "becomes the single thread of execution."

This is an optimization (removes GC/other-goroutine scheduling jitter from
the reactor's hot path), not a correctness fix — everything already worked
without it. Documented limitation: pinning guarantees the reactor's thread
stays *on* the given core, not that the core is *exclusive* to it; true
exclusivity needs deployment-level isolation (Linux `isolcpus=`/cgroup
`cpuset`), out of this package's scope.

## Phase 4 — Explicit I/O Layer On<Event> Lifecycle Hooks

### What shipped, real and tested
- `infra/transport/iouring.Client`: added explicit `OnConnected`, `OnDisconnected`, and `OnConnectError` lifecycle hooks.
- `infra/transport/iouring.conn`: updated connection lifecycle and `onDead(err)` callback to propagate disconnect causes cleanly.
- `infra/transport/iouring.Server`: added `OnConnected(connFD, remoteAddr)`, `OnDisconnected(connFD, err)`, and `OnConnectError(err)` lifecycle hooks.
- `infra/storage/journal.Store`: added `OnStorageError(err)` lifecycle hook and made `Close()` idempotent.
- Verified: `TestServerClient_LifecycleHooks` in `infra/transport/iouring` and `TestStore_OnStorageError_FiresOnDiskError` in `infra/storage/journal`.

## Phase 5 — Pure Event-Driven Transport & Storage Decoupling

### What shipped, real and tested
- `infra/transport/iouring`:
  - Completely decoupled transport layer from domain RPC message types.
  - Added `server.OnMessage(connFD, hdr, body)` and `server.Send(connFD, msgID, corrID, body)`.
  - Added `client.OnMessage(id, hdr, body)` and `client.Send(id, msgID, corrID, body)`.
  - Symmetrical 4-hook interface on both Client and Server.
- `infra/pool`:
  - Created generic `ArrayPool[T]` interface (`Rent(minCap)`, `Return(buf)`).
  - Implemented `BucketArrayPool[T]` with power-of-two capacity bucketing (16, 32, 64, 128, 256, 512, 1024, 2048, 4096...).
  - Full unit test suite in `infra/pool/array_pool_test.go`.
- `infra/storage/journal`:
  - Converted to pure raw-byte Key-Value WAL engine (`key []byte, val []byte`).
  - Zero domain knowledge (no `SiblingSet`, `VectorClock`, or `NodeID`).
  - Pure Command-Event model: `Get(reqID, key)`, `Put(reqID, key, val)`, `Delete(reqID, key)`, `Scan(scanID, start, end)`.
  - 4 event hooks with setter methods: `SetOnReadComplete`, `SetOnWriteComplete`, `SetOnScanComplete`, `SetOnStorageError`.
  - Integrated `pool.ArrayPool[ScanEntry]` for lock-free, zero-allocation, concurrent-safe in-flight scan batching.
- `engine/storage`:
  - Defined event-driven `KVStore` port interface over `journal.ScanEntry`.
  - Implemented `Adapter` (`storage.NewAdapter(rawStore, nodeID)`) managing domain `SiblingSet` binary serialization, vector clock reconciliation, and TTL filtering over any `KVStore`.
- Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 6 — Batched Parallel io_uring Scan with Forward-Sweep Offset Sorting

### What shipped, real and tested
- [x] 1. Designed `scanReadItem` struct tracking `{ slotIndex int, offset int64, length uint32 }`.
- [x] 2. Updated `inFlightScan` state to track `{ items []ScanEntry, pendingCount int, failedErr error }`.
- [x] 3. In `Scan(scanID, start, end)`:
  - Sorts read items by `offset` ascending (Physical Forward-Sweep).
  - Submits all `SubmitPread` SQEs concurrently in 1 batch.
- [x] 4. In `HandleCompletion`:
  - When each CQE arrives, decodes record and places directly into `items[slotIndex]` (preserving sorted key order).
  - Decrements `pendingCount`. When 0, fires `OnScanComplete` and recycles buffer to `scanPool`.
- [x] 5. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 7 — Adaptive Range Coalescing (Gap Clumping) for Scan

### What shipped, real and tested
- [x] 1. Defined `MaxCoalesceGap = 64 * 1024` (64KB) and `MaxChunkSize = 2 * 1024 * 1024` (2MB).
- [x] 2. Implemented `coalesceReadItems`:
  - Groups adjacent forward-sorted read items into coalesced spans when gap between records $\le$ `MaxCoalesceGap`.
  - Tracks `{ slotIndex int, relOffset uint32, recordLen uint32 }` per item within the chunk buffer.
- [x] 3. Updated `Scan` to submit $M$ chunk `SubmitPread` SQEs (where $M \le N$) instead of individual reads.
- [x] 4. In `HandleCompletion`:
  - On chunk CQE, unmarshals/unpacks all sub-records from `chunkBuf[relOffset : relOffset+recordLen]` directly into `scanState.items[slotIndex]`.
  - Decrements `pendingCount`. When 0, fires `OnScanComplete` and returns buffer to `scanPool`.
- [x] 5. Added unit test `TestStore_Scan_CoalescedDenseAndSparseRecords` in `infra/storage/journal/store_test.go`.
- [x] 6. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 8 — Contiguous Slot Table Model & Bit-Packed In-Flight Tracking

### What shipped, real and tested
- [x] 1. Replaced 5 structs with 2 flat structs: `scanItem` `{ keyIndex, offset, length }` and `scanChunk` `{ offset, length, startItem, endItem, buf }`.
- [x] 2. Eliminated `coalescedItemRef`, `coalescedChunk`, and `inFlightScanChunk` structs.
- [x] 3. Deleted secondary `inFlightScanChunks` map from `Store`.
- [x] 4. Encoded `(1 << 62) | (scanID << 16) | chunkIdx` into `io_uring` `UserData` for direct $O(1)$ chunk dispatch.
- [x] 5. Implemented `coalesceScanItems` using index range `[startItem, endItem)` with zero nested slice allocations.
- [x] 6. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 9 — WAL Compaction & OnCompactComplete Event Lifecycle

### What shipped, real and tested
- [x] 1. Defined `CompactFilter func(key, val []byte) (keep bool, newVal []byte)` and `CompactStats` in `infra/storage/journal`.
- [x] 2. Implemented `Store.Compact(compactID uint64, filter CompactFilter) error`:
  - Writes live, un-discarded records sequentially to `.compact` temporary WAL file.
  - Performs atomic `os.Rename` and descriptor swap.
  - Resets in-memory index offsets to contiguous offsets from 0.
  - Fires `OnCompactComplete(compactID, stats, nil)`.
- [x] 3. Updated `engine/storage.KVStore` port interface with `Compact` and `SetOnCompactComplete`.
- [x] 4. Updated `engine/storage.Adapter` with domain-level compaction integration (pruning tombstones and expired TTL siblings) and added `Reconcile` in `types.go`.
- [x] 5. Added comprehensive unit tests in `infra/storage/journal/store_test.go` (`TestStore_Compact`) and `engine/storage/adapter_test.go` (`TestAdapter_Compact`).
- [x] 6. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 10 — Circular Ring Buffer Multi-Segment WAL Engine

### What shipped, real and tested
- [x] 1. Updated `indexEntry` to include `SegID uint16`, `Offset int64`, and `Length uint32` in `infra/storage/journal/index.go`.
- [x] 2. Updated `Options` with `DataDir string`, `NumSegments int` (default 4), and `SegmentSize uint64` (default 64MB).
- [x] 3. Refactored `Store` to manage an array of permanent open file descriptors (`files []*os.File`, `fds []int`), `activeSeg` (Head), `tailSeg` (Tail), and `writeOffset`.
- [x] 4. Implemented Segment Rotation on write overflow (`writeOffset + len(buf) > SegmentSize` advances `activeSeg = (activeSeg + 1) % NumSegments` and resets `writeOffset = 0`).
- [x] 5. Updated `Get` to read directly from `fds[entry.SegID]` at `entry.Offset` in $O(1)$.
- [x] 6. Updated `Scan` with Range Coalescing per segment file and direct multi-segment `io_uring` dispatch.
- [x] 7. Implemented `Compact` to migrate live records from inactive segments to the active write head and advance `tailSeg`.
- [x] 8. Updated `ReplayRingSegments` to recover state across all segment files in the directory.
- [x] 9. Included ASCII architecture and lifecycle diagrams in codebase comments.
- [x] 10. Added unit tests in `store_test.go` (`TestStore_SegmentRotation_WrapsAroundRing`, `TestStore_MultiSegment_Scan`, `TestStore_MultiSegment_Compaction`).
- [x] 11. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 11 — 16-Byte Segment Header & Epoch-Based Head/Tail Discovery

### What shipped, real and tested
- [x] 1. Defined `SegmentHeaderSize = 16` and `SegmentMagic = [4]byte{'Q', 'U', 'O', 'R'}` in `infra/storage/journal`.
- [x] 2. Implemented `EncodeSegmentHeader(epoch uint64) []byte` and `DecodeSegmentHeader(buf []byte) (epoch uint64, ok bool)` in `header.go`.
- [x] 3. Updated `ReplaySingleSegment` to read segment header at offset 0 and replay records starting from `SegmentHeaderSize`.
- [x] 4. Updated `ReplayRingSegments` to discover `HEAD = argmax(epoch)` and `TAIL = argmin(epoch)` across all segments with $O(1)$ startup inspection.
- [x] 5. Updated `Store.Put` to stamp new `epoch` on segment rotation at offset 0, and start records at offset 16.
- [x] 6. Updated `Store.Compact` to preserve/stamp segment headers.
- [x] 7. Added unit tests verifying epoch header encoding/decoding, crash recovery head/tail discovery, and rotation stamping in `header_test.go` and `store_test.go` (`TestStore_EpochRecovery_RecoversHeadAndTailOnReopen`).
- [x] 8. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 12 — Status-Guided Log-Structured Replay & Dynamic Offset Discovery

### What shipped, real and tested
- [x] 1. Added `SegmentStatus` enum (`StatusEmpty`, `StatusWriter`, `StatusCompacted`) to `header.go` and updated `EncodeSegmentHeader` / `DecodeSegmentHeader`.
- [x] 2. Updated `header_test.go` with status round-trip tests.
- [x] 3. Refactored `ReplayRingSegments` in `replay.go`:
  - Pass 1: Scans 16-byte headers to identify `LatestCompacted` (Base) and `LatestWriter` (Head).
  - Pass 2: Replays Base checkpoint segment first, followed by subsequent `StatusWriter` segments in chronological epoch order.
  - Dynamically discovers `writeOffset` in the active Head segment by validating CRC records.
- [x] 4. Updated `store.go` to stamp `StatusWriter` on rotations and `StatusCompacted` on compaction.
- [x] 5. Added unit tests for status-guided checkpoint recovery in `replay_test.go` (`TestReplayRingSegments_StatusCompactedAnchorSkipsOlderSegments`) and `store_test.go`.
- [x] 6. Added crash-safe `file.Truncate(16)` before header write on rotations to eliminate ghost record replay (`TestStore_SegmentRotation_TruncatePreventsGhostRecords`).
- [x] 7. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.

## Phase 13 — 100% Zero-Allocation Disk I/O Engine

### What shipped, real and tested
- [x] 1. Implemented `DecodeRecordViews` and `EncodeRecordTo` in `infra/storage/journal/record.go` (single-pass in-place encoding & zero-copy subslice decoding).
- [x] 2. Updated `record_test.go` with zero-alloc view tests (`TestEncodeRecordTo_And_DecodeRecordViews_ZeroAllocs`).
- [x] 3. Integrated `bytePool`, `itemPool`, and `chunkPool` (`pool.BucketArrayPool`) into `Store` in `store.go`.
- [x] 4. Created generic, reusable `pool.SlotTable[T]` in `infra/pool/slot_table.go` and integrated it into `store.go` replacing Go maps.
- [x] 5. Refactored `Store.Get`, `Store.Put`, and `Store.Scan` to rent/return all buffers from pools with zero heap allocations.
- [x] 6. Added zero-alloc regression tests verifying 0 memory allocations on `Get`, `Put`, `Scan` in `store_test.go` (`TestStore_ZeroAlloc_Operations`).
- [x] 7. Implemented chunk-chained `pool.ByteArena` in `infra/pool/arena.go` and connected it to `Store.Scan` for zero-alloc result packing (`TestByteArena_MultiChunkOverflow_PointersRemainStable`).
- [x] 8. Full `make test`, `make vet`, and `make build` pass clean across all 39 packages.



