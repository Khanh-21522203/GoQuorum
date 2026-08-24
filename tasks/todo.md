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

## Phase 14 — Zero-Allocation Network I/O & Shared Buffer Pool

### What shipped, real and tested
- [x] 1. Extracted wire protocol codecs from `infra/transport/iouring` to `contracts/wire/` (`status.go`, `rpc.go`, `rpc_test.go`).
- [x] 2. Removed `FrameEncodedLen`, inlined framing calculations in `frame.go`, and moved test helpers to `frame_test.go`.
- [x] 3. Separated zero-alloc sliding buffer `Reassembler` into `reassembler.go` and `reassembler_test.go`.
- [x] 4. Embedded `Reassembler` by value in `tcpConn`, eliminating heap pointer allocations on socket connection.
- [x] 5. Refactored `ClientHandler` and `TransportAdapter` with `SlotTable[pendingRPC]` demultiplexing.
- [x] 6. Unified `BucketArrayPool[byte]` across `Client`, `Server`, and `TransportAdapter` so all network I/O shares a single buffer pool.
- [x] 7. Updated `server/app/server.go` composition root to inject the shared byte pool.
- [x] 8. Added `TestSharedBucketArrayPool_ClientServerAdapter` unit test and verified all tests/vet clean across all 39 packages.

## Phase 15 — Move Adapter to Engine Layer & Lower-Level Contract Alignment

### What shipped, real and tested
- [x] 1. `infra/transport/iouring` owns low-level I/O contracts and engines (`Client`, `Server`, `ClientHandler`, `ServerHandler`, `FrameHeader`).
- [x] 2. `engine/transport.Adapter` lives in `engine/transport/adapter.go`, wrapping `*iouring.Client` and implementing `iouring.ClientHandler` and `transport.Transport`.
- [x] 3. `engine/transport.Adapter` owns higher-level domain coordination: RPC correlation slots, request timeout timers, and wire codec marshaling.
- [x] 4. Removed `infra/transport/iouring/adapter.go`.
- [x] 5. Extracted `SiblingSet` (`siblings.go`) and `GossipEntry` (`gossip.go`) to `contracts/wire`, ensuring `contracts` has 0 dependencies on `engine`.
- [x] 6. Added comprehensive unit tests in `engine/transport/adapter_test.go`.
- [x] 7. Updated `server/app/server.go` and `conn_loopback_test.go` to use `enginetransport.NewAdapter`.
- [x] 8. Verified all tests and `go vet` clean across all 39 packages.

## Phase 16 — Layer 3 Adapter Decoupling & Zero-Alloc Refinement

### Context & Goals
- Refactor `engine/storage.Adapter` from heap `map[uint64]` to `pool.SlotTable[pendingStorageOp]` to match the zero-allocation pattern of `transport.Adapter` and `journal.Store`.
- Clean up `engine/transport.Adapter`'s `pendingRPC` representation to reduce struct footprint and streamline RPC reply demuxing.
- Verify zero regressions and full test suite pass across all packages.

### What shipped, real and tested
- [x] 1. Refactored `engine/storage.Adapter` to use `pool.SlotTable` for in-flight reads, writes, scans, and compactions, eliminating heap map allocations.
- [x] 2. Streamlined `pendingRPC` in `engine/transport.Adapter`, reducing callback field footprint with unified `onErrDone` handling.
- [x] 3. Verified full test suite pass with `-count=1` across all 39 packages (`go test -count=1 goquorum.io/v2/...`).
- [x] 4. Documented results in `tasks/todo.md`.

## Phase 17 — Move Full storage/ and transport/ Packages into engine/adapter/

### Context & Goals
- Move entire `storage/` and `transport/` packages from `engine/` into `engine/adapter/storage` and `engine/adapter/transport`.
- Eliminate root `engine/storage` and `engine/transport` folders.
- Update all 20+ importing packages across the workspace to use `goquorum.io/v2/engine/adapter/storage` and `goquorum.io/v2/engine/adapter/transport`.
- Verify `go test -count=1 goquorum.io/v2/...` and `go vet goquorum.io/v2/...` are 100% green.

### What shipped, real and tested
- [x] 1. Moved all files in `engine/storage/` (`storage.go`, `types.go`, `types_test.go`, `adapter.go`, `adapter_test.go`, `doc.go`) into `engine/adapter/storage/`.
- [x] 2. Moved all files in `engine/transport/` (`transport.go`, `adapter.go`, `adapter_test.go`, `doc.go`) into `engine/adapter/transport/`.
- [x] 3. Removed empty `engine/storage/` and `engine/transport/` directories.
- [x] 4. Updated all import paths across `engine/`, `infra/`, `server/`, and `gateway/` packages.
- [x] 5. Updated `server/app/server.go` composition root to cleanly use `storage` and `transport` packages from `engine/adapter/`.
- [x] 6. Ran full test suite and `go vet` across all packages (`go test -count=1 goquorum.io/v2/...` & `go vet goquorum.io/v2/...`), verifying 100% green.
- [x] 7. Documented results in `tasks/todo.md`.

## Phase 18 — Move Low-Level KVStore Contract to infra/storage

### Context & Goals
- Define low-level `KVStore` interface in `infra/storage/store.go` (IO Layer).
- Update `engine/adapter/storage` to consume `infrastorage.KVStore`.
- Verify `journal.Store` implements `infrastorage.KVStore`.
- Verify full test suite pass and 0 vet warnings.

### What shipped, real and tested
- [x] 1. Created `v2/infra/storage/store.go` with `KVStore` interface and callback type aliases.
- [x] 2. Updated `engine/adapter/storage/storage.go` to alias `KVStore` from `infra/storage`.
- [x] 3. Ran full test suite and `go vet` across all workspace modules (`go test -count=1 goquorum.io/v2/...` & `go vet goquorum.io/v2/...`), verifying 100% green.
- [x] 4. Documented results in `tasks/todo.md`.

## Phase 20 — Purge Legacy Individual Closure Hooks & Wrapper Helpers from journal.Store

### Context & Goals
- Remove legacy individual `On...` closure fields (`OnReadComplete`, `OnWriteComplete`, `OnScanComplete`, `OnCompactComplete`, `OnStorageError`) from `journal.Store`.
- Remove legacy `SetOn...` methods and intermediate `notify...` helper wrappers from `journal.Store`.
- Keep ONLY `handler StoreHandler` and `SetHandler(h StoreHandler)` with direct call-site dispatch `if s.handler != nil { s.handler.On...(...) }` (exact replica of `transport.ClientHandler`).
- Update `journal/store_test.go` to use `StoreHandler`.
- Verify 100% green test suite pass and 0 vet warnings.

### What shipped, real and tested
- [x] 1. Cleaned `v2/infra/storage/journal/store.go` to remove all legacy `On...` fields, `SetOn...` methods, and `notify...` wrappers; all events now invoke `if s.handler != nil { s.handler.On...(...) }` directly at call sites.
- [x] 2. Updated `engine/adapter/storage.Adapter` handler methods to use `defer a.slots.Release(...)` and invoke `slot.Value.on...` directly without temp variables.
- [x] 3. Updated `v2/infra/storage/journal/store_test.go` so `testStore` implements `StoreHandler` and registers via `store.SetHandler(ts)`.
- [x] 4. Ran full test suite and `go vet` across all packages (`go test -count=1 goquorum.io/v2/...` & `go vet goquorum.io/v2/...`), verifying 100% green.
- [x] 5. Documented results in `tasks/todo.md`.

## Phase 21 — Update Network Transport Adapter to Release Slots at the End

### Context & Goals
- Refactor `engine/adapter/transport.Adapter`'s `OnFrame` and timeout handlers to use `defer a.slots.Release(...)`.
- Access callback functions directly through `slot.Value.on...` without temporary local variable assignments.
- Verify 100% green test suite pass across all packages and 0 vet warnings.

### What shipped, real and tested
- [x] 1. Updated `engine/adapter/transport/adapter.go`'s `OnFrame` to use `defer a.slots.Release(hdr.CorrelationID)` and invoke `slot.Value.on...` directly.
- [x] 2. Updated timeout callbacks (`RemotePut`, `RemoteGet`, `Heartbeat`, `GetMerkleRoot`, `NotifyLeaving`, `GossipExchange`) to use `defer a.slots.Release(slotID)` and invoke `s.Value.on...` directly.
- [x] 3. Ran full test suite and `go vet` across all packages (`go test -count=1 goquorum.io/v2/...` & `go vet goquorum.io/v2/...`), verifying 100% green.
- [x] 4. Documented results in `tasks/todo.md`.

## Phase 22 — Consolidate Storage & Transport into single engine/adapter Package

### Context & Goals
- Merge `engine/adapter/storage` and `engine/adapter/transport` directly into `package adapter` (`v2/engine/adapter`).
- Remove subdirectories `engine/adapter/storage/` and `engine/adapter/transport/`.
- Update all importing packages across the workspace to import single `"goquorum.io/v2/engine/adapter"`.
- Verify 100% green test suite pass across all 39 packages and 0 vet warnings.

### What shipped, real and tested
- [x] 1. Consolidated `engine/adapter/storage` and `engine/adapter/transport` into single `package adapter` (`v2/engine/adapter`):
  - `storage.go`: `Storage` interface, `StorageAdapter` struct, `NewStorageAdapter`, `StoreHandler` implementation.
  - `storage_types.go`: `Sibling`, `SiblingSet`, `Reconcile`, `ScanFunc`, `StorageStats` (with alias `Stats`).
  - `storage_types_test.go`: CRDT sibling set resolution & truncation tests.
  - `storage_test.go`: Full Put/Get/Delete/Scan/Compact tests for `StorageAdapter`.
  - `transport.go`: `Transport` interface, `GossipEntry` alias, `TransportAdapter` struct, `NewTransportAdapter`, `ClientHandler` implementation.
  - `transport_test.go`: Full RPC/timeout/hook tests for `TransportAdapter`.
  - `doc.go`: Package documentation.
- [x] 2. Removed subdirectories `v2/engine/adapter/storage` and `v2/engine/adapter/transport`.
- [x] 3. Updated import paths across all packages (`coordinator`, `failuredetector`, `gossip`, `handoff`, `antientropy`, `readrepair`, `pebble`, `httprpc`, `admin`, `internal`, `server/app`) to single `"goquorum.io/v2/engine/adapter"`.
- [x] 4. Ran full test suite (`go test -count=1 goquorum.io/v2/...`) and `go vet` (`go vet goquorum.io/v2/...`), verifying 100% green pass and 0 vet warnings.
- [x] 5. Documented results in `tasks/todo.md`.

## Phase 23 — Visual-First Documentation & Comment Refactor across Coordinator Layer

### Context & Goals
- Replace prose-heavy comments in `v2/engine/coordinator` and related engine subsystems (`readrepair`, `antientropy`, `failuredetector`, `handoff`, `gossip`) with visual ASCII architecture, lifecycle, and execution flow diagrams.
- Shorten accompanying docstrings to non-obvious details only.
- Verify 100% green test suite pass and 0 vet warnings.

### What shipped, real and tested
- [x] 1. Rewrote [`v2/engine/coordinator/doc.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/doc.go) with visual ASCII subsystem composition diagram.
- [x] 2. Rewrote [`v2/engine/coordinator/coordinator.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator.go) with visual ASCII diagrams for:
  - Coordinator lifecycle state machine (`[NotStarted] -> [Running] -> [Stopped]`)
  - Quorum resolution state machine (`[requestAwaiting] -> [requestSucceeded]/[requestFailed]`)
  - `Put` / `Delete` replica fan-out & ack resolution flow
  - `Get` quorum read, maximal sibling merging, and read-repair flow
  - Causal dominance rules for maximal sibling reconciliation
- [x] 3. Rewrote comments in related coordinator-layer subsystems:
  - [`v2/engine/readrepair/readrepair.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/readrepair/readrepair.go): ASCII repair flow diagram
  - [`v2/engine/antientropy/antientropy.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/antientropy/antientropy.go): ASCII building/running/stopped lifecycle & exchange flow diagrams
  - [`v2/engine/failuredetector/failuredetector.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/failuredetector/failuredetector.go): ASCII peer health transition state machine
  - [`v2/engine/handoff/handoff.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/handoff/handoff.go): ASCII replay loop flow diagram
  - [`v2/engine/gossip/gossip.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/gossip/gossip.go): ASCII round exchange & merge flow diagram
- [x] 4. Ran full test suite (`go test -count=1 goquorum.io/v2/...`) and `go vet` (`go vet goquorum.io/v2/...`), verifying 100% green pass and 0 vet warnings.
- [x] 5. Documented results in `tasks/todo.md`.

## Phase 24 — Centralize State Machine & Master Reactor Timers in Coordinator with Event-Driven Subsystems

### Context & Goals
- Refactor worker subsystems (`failuredetector`, `gossip`, `handoff`, `antientropy`) to remove direct `reactor.Reactor` and internal `statemachine.Machine` dependencies.
- Subsystems become pure protocol workers executing direct I/O via `adapter.Transport` / `adapter.Storage` and reporting domain events to handlers.
- `Coordinator` becomes the single master controller:
  - Encapsulates `MembershipManager` and acts as single source of truth for cluster state.
  - Owns the central peer health / cluster state machine (`Active -> Degraded -> Failed`).
  - Implements subsystem event handlers (`ProbeHandler`, `GossipHandler`).
  - Owns the single `reactor.Reactor` instance, managing all periodic timers centrally.
- Update tests across all packages to verify 100% pass and 0 vet warnings.

### What shipped, real and tested
- [x] 1. Refactored [`engine/failuredetector`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/failuredetector/failuredetector.go):
  - Defined `ProbeHandler` interface (`OnHeartbeatResult(nodeID, err)`).
  - Removed `reactor`, timers, and internal `statemachine.Machine` from `FailureDetector`.
  - Exposed `Probe(peerIDs []node.NodeID)` and `ProbeOne(targetID node.NodeID)`.
  - Replaced test harness with pure synchronous unit tests in [`failuredetector_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/failuredetector/failuredetector_test.go).
- [x] 2. Refactored [`engine/gossip`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/gossip/gossip.go):
  - Defined `GossipHandler` interface (`OnGossipReceived(peerID, entries)`).
  - Removed `reactor`, timers, and internal `statemachine.Machine` from `Gossip`.
  - Exposed `Round(peers []node.NodeID, localEntries []adapter.GossipEntry)`.
  - Replaced test harness with pure synchronous unit tests in [`gossip_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/gossip/gossip_test.go).
- [x] 3. Refactored [`engine/handoff`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/handoff/handoff.go):
  - Removed `reactor`, timers, and internal `statemachine.Machine` from `HintedHandoff`.
  - Exposed `Replay(activePeers []node.NodeID)` and `StoreHint(targetNodeID, key, siblings)`.
  - Replaced test harness with pure synchronous unit tests in [`handoff_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/handoff/handoff_test.go).
- [x] 4. Refactored [`engine/antientropy`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/antientropy/antientropy.go):
  - Removed `reactor`, timers, and internal `statemachine.Machine` from `AntiEntropy`.
  - Exposed `Build() error`, `ScanTick(peerIDs []node.NodeID)`, `TriggerWithPeer(nodeID)`, and `SyncWithPeers(peers, done)`.
  - Replaced test harness with pure synchronous unit tests in [`antientropy_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/antientropy/antientropy_test.go).
- [x] 5. Refactored [`engine/coordinator`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator.go):
  - Encapsulated `MembershipManager` and exposed `Membership()`, `GetClusterView()`, `GetPeers()`, and `GetActivePeers()`.
  - Implemented `ProbeHandler` and `GossipHandler`, driving the central peer state machine (`Active -> Degraded -> Failed`) and updating membership + hash ring upon heartbeat / gossip events.
  - Centralized master reactor timers in `Start()` (`heartbeatTimer`, `gossipTimer`, `handoffTimer`, `antiEntropyTimer`) and cancelled in `Stop()`.
  - Folds hint writes on failed replica writes via `handoff.StoreHint`.
  - Maintained 100% passing tests in [`coordinator_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator_test.go).
- [x] 6. Verified seamless composition in [`server/app/server.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/server/app/server.go) and service APIs ([`server/api/admin.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/server/api/admin.go), [`server/api/internal.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/server/api/internal.go)).
- [x] 7. Ran full test suite across all workspace packages (`go test -count=1 goquorum.io/v2/...`) and `go vet`, verifying 100% green pass and 0 vet warnings.

## Phase 25 — Implement Switch-Based FSM with Enter() Hooks in Coordinator & Remove Generic Statemachine

### Context & Goals
- Refactor all 3 state machines in `v2/engine/coordinator/coordinator.go` to use the switch-based `Handle()` + `Enter()` pattern:
  1. Peer Liveness FSM (`Active` <-> `Degraded` <-> `Failed` -> `Leaving`) with `enterPeerState` updating `MembershipManager`, `HashRing`, and triggering hint replay.
  2. Request Quorum Resolution FSM (`writeRequest` & `readRequest`) with `enterWriteRequestState` / `enterReadRequestState` handling timer cancellation and callback resolution.
  3. Coordinator Subsystem Lifecycle FSM (`NotStarted` -> `Running` -> `Stopped`) with `enterLifecycle` managing build and timer arm/disarm.
- Remove `v2/engine/statemachine` module/package as it is superseded by zero-allocation switch FSMs.
- Verify 100% test pass across all workspace packages with 0 vet warnings.

### What shipped, real and tested
- [x] 1. Refactored [`v2/engine/coordinator/coordinator.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator.go):
  - Refactored Peer Liveness FSM to switch-based `handlePeerTrigger`, `transitionPeer`, and `enterPeerState` (updating `MembershipManager`, `HashRing`, and replaying hints on recovery).
  - Refactored Request Quorum Resolution FSM (`writeRequest` & `readRequest`) to switch-based `handleWriteRequest` / `handleReadRequest` with zero heap allocations on client RPC paths.
  - Refactored Coordinator Lifecycle FSM to switch-based `handleLifecycle`, `transitionLifecycle`, and `enterLifecycle`.
  - Completely eliminated `statemachine.Machine` dependency from `coordinator.go`.
- [x] 2. Removed `v2/engine/statemachine/` package (`doc.go`, `machine.go`, `machine_test.go`).
- [x] 3. Ran full test suite across all workspace packages (`go test -count=1 goquorum.io/v2/...`) and `go vet`, verifying 100% green pass and 0 vet warnings.
- [x] 4. Documented results in `tasks/todo.md`.

## Phase 26 — Modularize Coordinator FSMs into Dedicated Source Files

### Context & Goals
- Split `v2/engine/coordinator/coordinator.go` into focused, cohesive source files:
  1. `v2/engine/coordinator/coordinator.go`: Main struct, options, constructor, public client APIs (`Put`, `Get`, `Delete`), helper queries.
  2. `v2/engine/coordinator/fsm_peer.go`: Peer liveness FSM (state, triggers, `handlePeerTrigger`, `transitionPeer`, `enterPeerState`, gossip handler).
  3. `v2/engine/coordinator/fsm_request.go`: Quorum write/read request resolution FSM (`writeRequest`, `readRequest`, `handleWriteRequest`, `handleReadRequest`, `enterWriteRequestState`, `enterReadRequestState`).
  4. `v2/engine/coordinator/fsm_lifecycle.go`: Subsystem lifecycle FSM (`coordinatorState`, `coordinatorTrigger`, `Start`, `Stop`, `armTimers`, `disarmTimers`).
- Maintain 100% test coverage and verify zero regressions.

### What shipped, real and tested
- [x] 1. Created [`v2/engine/coordinator/fsm_peer.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_peer.go): encapsulates peer health transitions (`Active`, `Degraded`, `Failed`, `Leaving`), `ProbeHandler`, `GossipHandler`, and hint flush on recovery.
- [x] 2. Created [`v2/engine/coordinator/fsm_request.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_request.go): encapsulates in-flight quorum write & read request lifecycles with zero allocations.
- [x] 3. Created [`v2/engine/coordinator/fsm_lifecycle.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_lifecycle.go): encapsulates subsystem lifecycle (`Start`, `Stop`, timer management).
- [x] 4. Created dedicated unit test suites:
  - [`v2/engine/coordinator/fsm_peer_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_peer_test.go): tests peer state transitions (`Active` -> `Degraded` -> `Failed`), hint buffering during failure, automatic hint replay upon recovery, and gossip updates.
  - [`v2/engine/coordinator/fsm_request_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_request_test.go): tests write/read request state transitions (`Awaiting` -> `Succeeded` / `Failed`), straggler handling after resolution, and client timeouts.
  - [`v2/engine/coordinator/fsm_lifecycle_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_lifecycle_test.go): tests lifecycle startup (`NotStarted` -> `Running`), timer arming, and graceful shutdown (`Stop` -> `Stopped`).
- [x] 5. Refactored [`v2/engine/coordinator/coordinator.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator.go): trimmed down to core coordinator composition, client APIs (`Put`, `Get`, `Delete`), and membership query helpers.
- [x] 6. Ran full test suite across all workspace packages (`go test -count=1 goquorum.io/v2/...`) and `go vet`, verifying 100% green pass and 0 vet warnings.

## Phase 27 — Decouple FSMs into Standalone Pure Structs with Event Hook Callbacks

### Context & Goals
- Refactor state machines (`PeerFSM`, `LifecycleFSM`, `RequestTracker`) so they have **zero dependencies on Coordinator**:
  - `PeerFSM`: Pure struct tracking peer states, firing `PeerTransitionHandler(id, from, to)`.
  - `LifecycleFSM`: Pure struct tracking subsystem runtime states, firing `LifecycleTransitionHandler(from, to)`.
  - `RequestTracker`: Pure request tracker managing write & read quorum state machines.
- The `Coordinator` owns instances of these FSMs and registers event listener callbacks on them to execute side-effects (updating `MembershipManager`, `HashRing`, and flushing `HintedHandoff`).
- Update unit tests and verify 100% test coverage with zero vet warnings.

### What shipped, real and tested
- [x] 1. Refactored [`v2/engine/coordinator/fsm_peer.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_peer.go):
  - Defined `PeerTransitionHandler func(id node.NodeID, from, to node.NodeState)`.
  - Implemented standalone `PeerFSM` struct (`NewPeerFSM`, `AddPeer`, `GetPeer`, `Peers`, `OnHeartbeatResult`, `OnGossipReceived`).
  - Zero dependencies on Coordinator, Storage, or HashRing.
- [x] 2. Refactored [`v2/engine/coordinator/fsm_lifecycle.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_lifecycle.go):
  - Defined `LifecycleTransitionHandler func(from, to coordinatorState) error`.
  - Implemented standalone `LifecycleFSM` struct (`NewLifecycleFSM`, `Start`, `Stop`, `State`).
  - Zero dependencies on Coordinator, Storage, or Timers.
- [x] 3. Refactored [`v2/engine/coordinator/fsm_request.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_request.go):
  - Decoupled `writeRequest` and `readRequest` into standalone request FSMs (`handleResult`, `handleTimeout`, `isDone`).
- [x] 4. Updated [`v2/engine/coordinator/coordinator.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator.go):
  - Wired `peerFSM` and `lifecycleFSM` in `NewCoordinator`.
  - Implemented event listeners `onPeerTransition` and `onLifecycleTransition` to execute cluster side-effects.
- [x] 5. Updated unit tests ([`fsm_peer_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_peer_test.go), [`fsm_lifecycle_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_lifecycle_test.go), [`fsm_request_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/fsm_request_test.go)):
  - Added pure isolation unit tests verifying FSM transitions without any Coordinator or IO dependencies.
- [x] 6. Ran full test suite across all workspace packages (`go test -count=1 goquorum.io/v2/...`) and `go vet`, verifying 100% green pass and 0 vet warnings.
- [x] 7. Documented results in `tasks/todo.md`.

## Phase 28 — Replace LifecycleFSM with Idiomatic Plain Start() and Stop() Functions

### Context & Goals
- Simplify Coordinator subsystem lifecycle by replacing ceremonial `LifecycleFSM` with standard idiomatic Go `Start()` / `Stop()` methods guarded by simple state booleans (`started`, `stopped`).
- Remove `v2/engine/coordinator/fsm_lifecycle.go` and `v2/engine/coordinator/fsm_lifecycle_test.go`.
- Add lifecycle startup & shutdown tests directly into `coordinator_test.go`.
- Run all workspace tests and verify 100% pass with 0 vet warnings.

### What shipped, real and tested
- [x] 1. Updated [`v2/engine/coordinator/coordinator.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator.go):
  - Replaced `lifecycleFSM` with `started bool` and `stopped bool` plus `IsRunning() bool`.
  - Implemented clean, idempotent `Start()` and `Stop()` methods.
- [x] 2. Removed `v2/engine/coordinator/fsm_lifecycle.go` and `v2/engine/coordinator/fsm_lifecycle_test.go`.
- [x] 3. Added `TestCoordinator_StartAndStop` to [`v2/engine/coordinator/coordinator_test.go`](file:///home/khanh.dao/Projects/SideProjects/GoQuorum/v2/engine/coordinator/coordinator_test.go).
- [x] 4. Ran full test suite across all workspace packages (`go test -count=1 goquorum.io/v2/...`) and `go vet`, verifying 100% green pass and 0 vet warnings.
- [x] 5. Documented results in `tasks/todo.md`.

## Phase 29 — Move Reactor to Infra (`v2/infra/reactor`)

### Context & Goals
- Move `v2/engine/reactor` to `v2/infra/reactor` to fix the layer inversion where low-level infrastructure packages (`ioruntime`, `storage/journal`, `transport/iouring`) imported `engine/reactor`.
- Group all execution runtimes and OS polling mechanisms inside `v2/infra/`.
- Update all import paths across `v2/`.
- Verify full test suite passing with 0 vet warnings.

### What shipped, real and tested
- [x] 1. Moved directory `v2/engine/reactor` to `v2/infra/reactor`.
- [x] 2. Updated all import paths across `v2/` from `goquorum.io/v2/engine/reactor` to `goquorum.io/v2/infra/reactor`:
  - `v2/engine/adapter/...`
  - `v2/engine/coordinator/...`
  - `v2/infra/ioruntime/...`
  - `v2/infra/storage/journal/...`
  - `v2/infra/transport/iouring/...`
  - `v2/server/app/...`
- [x] 3. Ran full test suite across all workspace packages (`go test -count=1 goquorum.io/v2/...`) and `go vet`, verifying 100% green pass and 0 vet warnings.
## Phase 30 — Split Client & Server Network Adapters with Event Hook Pattern

### Context & Goals
- Separate network adapters cleanly in `v2/engine/adapter/`:
  - `ClientAdapter` (Client): Pure outbound client adapter over `iouring.Client` implementing `adapter.ClientTransport`.
  - `ServerAdapter` (Server): Pure inbound server adapter over `iouring.Server` implementing `iouring.ServerHandler` with typed `ServerInboundHandler` event hooks.
- Update `Coordinator` to export `GetLocalGossipEntries()` for gossip exchanges and dispose `c.transport.Close()` on `Stop()`.
- Update `server/api/internal.go` so `InternalAPI` implements `adapter.ServerInboundHandler` routing replica data requests to `Storage` and cluster control requests to `Coordinator`.
- Update `server/app/server.go` composition root to cleanly compose `StorageAdapter`, `ClientAdapter` (Client), `Coordinator`, `ServerAdapter` (Server), and `InternalAPI`.
- Implement `config.Load(path string)` in `infra/config` with YAML parsing.
- Add comprehensive unit and loopback integration tests and verify 100% green pass and 0 vet warnings.

### What shipped, real and tested
- [x] 1. Renamed `Transport` interface to `ClientTransport` and `TransportAdapter` to `ClientAdapter` across `v2/engine/adapter/client_adapter.go`, `v2/engine/coordinator/`, `v2/engine/antientropy/`, `v2/engine/failuredetector/`, `v2/engine/gossip/`, `v2/engine/handoff/`, `v2/engine/readrepair/`, and `v2/server/app/server.go`.
- [x] 2. Created `v2/engine/adapter/server_adapter.go` defining `ServerInboundHandler` and `ServerAdapter` with typed event hooks for all 6 domain request types (`OnRemotePut`, `OnRemoteGet`, `OnHeartbeat`, `OnGetMerkleRoot`, `OnGossipExchange`, `OnNotifyLeaving`) and connection lifecycle hooks.
- [x] 3. Added unit tests for `ServerAdapter` in `v2/engine/adapter/server_adapter_test.go` and `ClientAdapter` in `client_adapter_test.go`.
- [x] 4. Updated `v2/server/api/internal.go` so `InternalAPI` implements `adapter.ServerInboundHandler` directly routing replica I/O to `Storage` and cluster control to `Coordinator`.
- [x] 5. Updated `v2/server/app/server.go` composition root to compose `StorageAdapter`, `ClientAdapter` (Client), `Coordinator`, `ServerAdapter` (Server), and `InternalAPI`.
- [x] 6. Implemented YAML `config.Load()` and `config.Validate()` in `v2/infra/config/config.go` with unit tests in `config_test.go`.
- [x] 7. Added multi-node loopback integration test `TestServer_TwoNode_ReplicationAndRead` in `v2/server/app/server_test.go` verifying 2-node quorum write, WAL persistence, remote ACK, and quorum read.
- [x] 8. Ran full workspace test suite with race detector (`go test -count=1 -race goquorum.io/v2/...`), `go vet`, and `go build`, verifying 100% green pass and 0 vet warnings.
- [x] 9. Documented results in `tasks/todo.md`.

## Phase 31 — Client Adapter Event Hook Pattern Refactoring

### Context & Goals
- Refactor `ClientAdapter` to use the pure **Event Hook Pattern** for all inbound traffic:
  - Define `ClientInboundHandler` interface (`OnRemotePutResponse`, `OnRemoteGetResponse`, `OnHeartbeatResponse`, `OnGetMerkleRootResponse`, `OnNotifyLeavingResponse`, `OnGossipExchangeResponse`, `OnPeerConnected`, `OnPeerDisconnected`, `OnPeerConnectError`).
  - Update `ClientTransport` interface with command methods (`SendRemotePut`, `SendRemoteGet`, `SendHeartbeat`, `SendGetMerkleRoot`, `SendNotifyLeaving`, `SendGossipExchange`, `SetInboundHandler`).
- Implement `adapter.ClientInboundHandler` on `coordinator.Coordinator` to receive and handle all client inbound event hooks directly.
- Update `failuredetector`, `gossip`, `handoff`, `readrepair`, and `antientropy` to integrate cleanly with `Coordinator` event handling.
- Update `server/app/server.go` to register `coord` as `clientAdapter.SetInboundHandler(coord)`.
- Update and run unit & loopback integration tests to verify 100% green pass and 0 vet warnings with race detector.

### Tasks
- [x] 1. Define `ClientInboundHandler` and update `ClientTransport` & `ClientAdapter` in `v2/engine/adapter/client_adapter.go`.
- [x] 2. Update unit tests in `v2/engine/adapter/client_adapter_test.go`.
- [x] 3. Update `Coordinator` in `v2/engine/coordinator/coordinator.go` and subsystems to implement `ClientInboundHandler`.
- [x] 4. Update coordinator tests in `v2/engine/coordinator/coordinator_test.go` and other engine packages.
- [x] 5. Wire `clientAdapter.SetInboundHandler(coord)` in `v2/server/app/server.go`.
- [x] 6. Run full test suite with `-race` (`go test -count=1 -race goquorum.io/v2/...`), `go vet`, and `go build`.
- [x] 7. Document results in `tasks/todo.md`.

## Phase 32 — Unified Adapter Handlers (ClientAdapterHandler & ServerAdapterHandler)

### Context & Goals
- Rename `ClientInboundHandler` -> `ClientAdapterHandler` and `ServerInboundHandler` -> `ServerAdapterHandler`.
- Elevate server connection lifecycle hooks (`OnClientConnected(connFD, remoteAddr)`, `OnClientDisconnected(connFD, err)`) into `ServerAdapterHandler` for unified symmetry.
- Update `InternalAPI` in `server/api/internal.go` to implement `ServerAdapterHandler`.
- Update `Coordinator` in `engine/coordinator/coordinator.go` to implement `ClientAdapterHandler`.
- Update all adapter and engine tests and verify 100% green pass with race detector.

### Tasks
- [x] 1. Update `v2/engine/adapter/client_adapter.go` (`ClientAdapterHandler`) and `v2/engine/adapter/server_adapter.go` (`ServerAdapterHandler`).
- [x] 2. Update `v2/engine/coordinator/coordinator.go` and `v2/server/api/internal.go`.
- [x] 3. Update unit tests in `v2/engine/adapter/`, `v2/engine/coordinator/`, `v2/server/`.
- [x] 4. Run full test suite with `-race` (`go test -count=1 -race goquorum.io/v2/...`), `go vet`, and `go build`.
- [x] 5. Document results in `tasks/todo.md`.

## Phase 33 — Encapsulate ClientAdapter in Coordinator & Remove from Server Struct

### Context & Goals
- Remove `clientAdapter *adapter.ClientAdapter` from `Server` struct in `server/app/server.go`.
- Let `Coordinator` fully manage and own its outbound `ClientTransport` (`NewCoordinator` auto-wires handler, `Coordinator.Start()` dials peers from membership, `Coordinator.Stop()` closes transport, `Coordinator.HandleCompletion(ev)` delegates reactor completion demuxing).
- Verify 100% tests pass with race detector.

### Tasks
- [x] 1. Update `Coordinator` in `v2/engine/coordinator/coordinator.go` to auto-wire handler, dial peers in `Start()`, and provide `HandleCompletion(ev)`.
- [x] 2. Clean up `v2/server/app/server.go` to remove `clientAdapter` field and redundant loops.
- [x] 3. Run full test suite with `-race` (`go test -count=1 -race goquorum.io/v2/...`), `go vet`, and `go build`.
- [x] 4. Document results in `tasks/todo.md`.

## Phase 34 — O(1) Direct FD Self-Registration Reactor Routing

### Context & Goals
- Add `RegisterFD(fd int, fn func(Event))` and `UnregisterFD(fd int)` to `infra/reactor/reactor.go`.
- In `Reactor.Run()`, extract `fd := int(uint32(ev.UserData >> 32))` and dispatch directly in $O(1)$ to `r.fdHandlers[fd]`, with fallback to `r.handler(ev)`.
- Update `iouring.Client` (`clientConn`), `iouring.Server` (`serverConn` + `listenFD`), and `journal.Store` to self-register their FDs on open and unregister on close.
- Remove the manual linear cascade demuxer from `server/app/server.go`.
- Verify full test suite with `-race` across all packages.

### Tasks
- [x] 1. Implement `RegisterFD` / `UnregisterFD` and $O(1)$ dispatch in `v2/infra/reactor/reactor.go` and unit tests in `reactor_test.go`.
- [x] 2. Update `v2/infra/transport/iouring/` (`Client`, `Server`, `clientConn`, `serverConn`) to self-register/unregister FDs with the reactor.
- [x] 3. Update `v2/infra/storage/journal/` (`Store`) to self-register/unregister segment FDs with the reactor using `makeUserData(fd, reqID)`.
- [x] 4. Remove manual completion demuxing chain from `v2/server/app/server.go` and clean up `Coordinator.HandleCompletion`.
- [x] 5. Run full workspace test suite with `-race` (`go test -count=1 -race goquorum.io/v2/...`), `go vet`, and `go build`.
- [x] 6. Document results in `tasks/todo.md`.









