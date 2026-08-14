# GoQuorum v2 — Conventions

These rules are binding for every module. They mirror the layout of GoBlob v2 and
SharpDB V2. Read this before writing any code, and read `INTERFACES.md` for the exact
cross-module contracts.

## Module system
- One directory per module under `v2/`; each has its own `go.mod`.
- Module path: `goquorum.io/v2/<module>`.
- Go version: `go 1.25.0`. No `toolchain` directive.
- Cross-module deps use `require <path> v0.0.0` **and** a relative `replace <path> => ../<dir>`.
  `replace` directives sit each on their own line, separated by blank lines (not a block).
- `v2/go.work` ties all modules together. Do not edit it.

## Dependency graph (imports may only point rightward)
```
contracts ──▶ engine ──▶ infra
                   │        │
                   ├──▶ gateway
                   ├──▶ client
                   └────────┴──▶ server ──▶ cli
                                        └──▶ test
```
- `contracts` imports only the standard library.
- `engine` imports only `contracts` and the standard library. It declares PORT interfaces.
- `infra`, `gateway`, `client` implement/consume engine ports.
- `server` composes `engine` + `infra` + `gateway`.
- `cli` and `test` are top-of-stack entrypoints.
- Never introduce an import that points leftward. There must be no cycles.

## Ports & adapters (the core rule)
- Interfaces that cross a module boundary are **declared in `engine`** (e.g. `Storage`,
  `Transport`). Concrete implementations live in `infra`.
- `engine` must never import Pebble, gRPC, HTTP, or any I/O library. It is pure domain logic.

## SCAFFOLD RULES (this phase only)
- This is a scaffold: **structure + interfaces + typed stubs**, not a working system.
- Every stub method returns `ErrNotImplemented` (or a zero value + that error) and carries a
  `// TODO(v2): ...` comment describing the real behaviour, referencing the v1 source file.
- **Do NOT import external dependencies yet** (no pebble, grpc, prometheus, yaml, etc.).
  Stubs must compile against the standard library + local modules only, so the whole
  workspace builds offline with no `go mod tidy`. Mark where each external dep will go
  with a `// TODO(v2): import <dep>` comment.
- `var ErrNotImplemented = errors.New("not implemented")` lives in `contracts` and is
  reused everywhere.
- The definition of done for the scaffold: `go build ./...` and `go vet ./...` are clean
  from the `v2/` directory.

## Naming
- Package name == directory name, lowercase, no underscores (`failuredetector`, not `failure_detector`).
- File names lowercase, words concatenated (`hashring.go`, `merkletree.go`), except Go's
  mandated `_test.go`.
- One `doc.go` per package containing only the `// Package X ...` comment and `package X`.
- Exported identifiers `PascalCase`. Carry v1 domain vocabulary verbatim (`NodeID`,
  `VectorClock`, `SiblingSet`, `Coordinator`, `HashRing`, `MembershipManager`).

## v2 improvements over v1 (apply these deliberately)
1. `engine.Storage` is an INTERFACE (v1 had only a concrete `*storage.Storage`).
2. `engine.Transport` replaces v1's misnamed `RPCClient`/`GRPCClient` (v1 transport was
   HTTP/JSON despite the name). Keep the method set; pick an honest name.
3. All config structs get `yaml` tags (v1 left several nested structs untagged, so they
   silently failed to deserialize).
4. `VectorClock` should not share its underlying map across value copies (v1 footgun).
   Document the intended value semantics in `contracts/vclock/doc.go`.
