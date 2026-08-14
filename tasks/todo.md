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

Verified: `TestLockToCore_PinsAffinityToExactlyOneCore` asserts against a
real `unix.SchedGetaffinity` readback (not a mock) that exactly one core is
set; `TestLockToCore_RejectsOutOfRangeCore` checks the bounds error. Full
`go build`/`go vet`/`go test -race` across the whole workspace stayed clean,
and `GOOS=darwin GOARCH=arm64 go build` confirms the `!linux` stub keeps this
portable.
