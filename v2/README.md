# GoQuorum v2

A ground-up rebuild of GoQuorum as a Go multi-module workspace, split along clean
architectural seams (ports & adapters). This directory is the v2 scaffold; v1 remains
in the repository root during migration.

## Modules

| Module | Path | Responsibility |
|---|---|---|
| `contracts` | `goquorum.io/v2/contracts` | Shared value types, errors, vector clocks, wire types. Leaf; stdlib only. |
| `engine` | `goquorum.io/v2/engine` | Pure domain: coordinator, hashring, membership, gossip, failure detection, anti-entropy, hinted handoff. Declares `Storage` / `Transport` **ports**. |
| `infra` | `goquorum.io/v2/infra` | Adapters: Pebble storage, HTTP transport, config loading, observability, security, backup. |
| `gateway` | `goquorum.io/v2/gateway` | HTTP/JSON gateway. |
| `server` | `goquorum.io/v2/server` | gRPC/admin/internal service implementations + composition root. |
| `client` | `goquorum.io/v2/client` | Go client library. |
| `cli` | `goquorum.io/v2/cli` | `quorum` daemon + `quorumctl` control tool. |
| `test` | `goquorum.io/v2/test` | Cross-module harness, integration tests, benchmarks. |

## Dependency direction

```
contracts ─▶ engine ─▶ {infra, gateway, client} ─▶ server ─▶ {cli, test}
```

Imports may only point rightward. `engine` defines interfaces; `infra` implements them.
See `CONVENTIONS.md` for the rules and `INTERFACES.md` for the exact cross-module contracts.

## Status

Scaffold. Every method is a typed stub returning `ErrNotImplemented`.
`go build goquorum.io/v2/...` and `go vet goquorum.io/v2/...` are green.
Implementation is tracked in `../tasks/todo.md`.

## Development

```sh
cd v2
make build   # go build goquorum.io/v2/...
make vet     # go vet goquorum.io/v2/...
make test    # go test goquorum.io/v2/...
```

Note: `v2/` is a workspace root, not a module, so use the `goquorum.io/v2/...`
package prefix (as the Makefile does) rather than `./...`.
