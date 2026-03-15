# Feature: CLI Admin Tool

## 1. Purpose

GoQuorum currently exposes cluster management entirely through gRPC and HTTP APIs. Operators need a command-line tool to inspect cluster state, trigger operations, and debug issues without writing code or curling raw HTTP endpoints. The `quorumctl` CLI provides a human-friendly interface to all admin and client API operations: checking cluster health, getting and setting keys, viewing key info, inspecting the ring, and triggering maintenance tasks. This feature is not yet implemented.

## 2. Responsibilities

- **Key operations**: `get`, `put`, `delete` commands for interactive key-value access from the shell
- **Cluster inspection**: `status` (cluster health + node list), `ring` (hash ring dump), `key-info` (replica placement for a key)
- **Maintenance commands**: `compaction` (trigger LSM compaction), `backup` / `restore` (invoke backup admin RPC)
- **Configuration**: Accept `--address`, `--tls-cert`, `--tls-key`, `--tls-ca` flags for connecting to any node
- **Output formats**: Human-readable table output by default; `--output json` for scripting
- **Exit codes**: Exit 0 on success, 1 on application error (key not found, quorum failure), 2 on usage error

## 3. Non-Responsibilities

- Does not implement a REPL or interactive shell
- Does not implement cluster bootstrapping or node provisioning
- Does not replace Prometheus/Grafana for metrics visualization
- Does not implement watch/subscribe (no polling loop in CLI)

## 4. Architecture Design

```
quorumctl [global flags] <command> [flags] [args]

Global flags:
  --address string    GoQuorum node address (default "localhost:7070")
  --timeout duration  RPC timeout (default 5s)
  --output string     Output format: "text" or "json" (default "text")
  --tls-ca string     Path to CA certificate
  --tls-cert string   Path to client certificate (for mTLS)
  --tls-key string    Path to client key (for mTLS)

Commands:
  get <key>                        Get a key's value(s)
  put <key> <value> [--ttl 30s]   Set a key's value
  delete <key>                     Delete a key
  status                           Show cluster health and node list
  ring [--key <key>]               Show hash ring; if --key given, show preference list
  key-info <key>                   Show per-replica status for a key
  compact                          Trigger LSM compaction on the target node
  backup create                    Create a backup on the target node
  backup list                      List available backups
  backup restore <backup-id>       Restore from a backup
  version                          Print quorumctl and server version
```

## 5. Core Data Structures (Go)

```go
// quorumctl is a standalone binary in cmd/quorumctl/
package main

import (
    "github.com/spf13/cobra"
    "github.com/spf13/viper"
    pb "goquorum/api/proto"
)

// CLIConfig holds global connection settings parsed from flags.
type CLIConfig struct {
    Address  string
    Timeout  time.Duration
    Output   string // "text" or "json"
    TLSCAert string
    TLSCert  string
    TLSKey   string
}

// Clients bundles the gRPC stubs needed by CLI commands.
type Clients struct {
    client pb.GoQuorumClient
    admin  pb.GoQuorumAdminClient
    conn   *grpc.ClientConn
}
```

## 6. Public Interfaces

The CLI is a binary (`quorumctl`), not a library. Its public interface is its command-line surface:

```
# Get a key
quorumctl get session:abc

# Put a key with a TTL
quorumctl put session:abc '{"user": 42}' --ttl 30m

# Delete a key (reads causal context first, then deletes)
quorumctl delete session:abc

# Check cluster health
quorumctl status
quorumctl status --output json

# Inspect the ring
quorumctl ring
quorumctl ring --key session:abc   # show which nodes own this key

# Per-replica status for a key
quorumctl key-info session:abc

# Trigger compaction
quorumctl compact

# Backup operations
quorumctl backup create
quorumctl backup list
quorumctl backup restore node1/2026-01-15T10:30:00Z
```

## 7. Internal Algorithms

### get command
```
RunGet(cmd, args):
  key = args[0]
  ctx, cancel = context.WithTimeout(background, cfg.Timeout)
  defer cancel()

  resp, err = clients.client.Get(ctx, &pb.GetRequest{Key: key})
  if err != nil:
    if isNotFound(err): fatal("key not found: %s", key)
    fatal("get failed: %v", err)

  if cfg.Output == "json":
    printJSON(resp)
  else:
    for i, sib in resp.Siblings:
      fmt.Printf("Sibling %d:\n  Value: %s\n  Tombstone: %v\n", i+1, sib.Value, sib.Tombstone)
    fmt.Printf("Context: %x\n", resp.Context)
```

### status command
```
RunStatus(cmd, args):
  ctx, cancel = context.WithTimeout(background, cfg.Timeout)
  defer cancel()

  health, err = clients.admin.Health(ctx, &pb.HealthRequest{})
  if err != nil: fatal("health check failed: %v", err)

  cluster, err = clients.admin.ClusterInfo(ctx, &pb.ClusterInfoRequest{})
  if err != nil: fatal("cluster info failed: %v", err)

  if cfg.Output == "json":
    printJSON(map{health, cluster})
    return

  // Text table
  fmt.Printf("Status: %s\n\n", health.Status)
  fmt.Printf("%-20s %-25s %-10s\n", "NODE ID", "ADDRESS", "STATE")
  for _, node in cluster.Nodes:
    fmt.Printf("%-20s %-25s %-10s\n", node.Id, node.Address, node.State)
```

### delete command (requires prior Get for causal context)
```
RunDelete(cmd, args):
  key = args[0]
  ctx, cancel = context.WithTimeout(background, cfg.Timeout)
  defer cancel()

  // First, Get to obtain causal context
  getResp, err = clients.client.Get(ctx, &pb.GetRequest{Key: key})
  if err != nil:
    if isNotFound(err): fatal("key not found: %s", key)
    fatal("get failed: %v", err)

  // Then Delete with the context
  _, err = clients.client.Delete(ctx, &pb.DeleteRequest{Key: key, Context: getResp.Context})
  if err != nil: fatal("delete failed: %v", err)
  fmt.Printf("Deleted: %s\n", key)
```

## 8. Persistence Model

`quorumctl` is a stateless CLI binary. It holds no local state between invocations. Connection parameters may be read from a config file at `~/.quorumctl.yaml` (via viper) to avoid repeating `--address` on every call.

### Config file (`~/.quorumctl.yaml`)
```yaml
address: node1.example.com:7070
timeout: 10s
tls_ca: /etc/goquorum/ca.crt
```

## 9. Concurrency Model

Not applicable. The CLI is single-threaded and single-request-per-invocation. No concurrency primitives are needed.

## 10. Configuration

All configuration via CLI flags (highest priority) or `~/.quorumctl.yaml` (lower priority):

| Flag | Config key | Default | Description |
|------|------------|---------|-------------|
| `--address` | `address` | `localhost:7070` | Target node gRPC address |
| `--timeout` | `timeout` | `5s` | Per-RPC deadline |
| `--output` | `output` | `text` | Output format |
| `--tls-ca` | `tls_ca` | (none) | CA certificate path |
| `--tls-cert` | `tls_cert` | (none) | Client certificate (mTLS) |
| `--tls-key` | `tls_key` | (none) | Client key (mTLS) |

## 11. Observability

Not applicable for a CLI tool. Exit codes convey outcome:
- `0` — success
- `1` — application error (key not found, quorum error, RPC failure)
- `2` — usage error (invalid arguments, missing required flag)

Error messages are written to stderr; output data to stdout (enabling shell piping).

## 12. Testing Strategy

- **Unit tests**:
  - `TestGetCommand_Success`: mock gRPC server returns sibling, assert formatted output
  - `TestGetCommand_NotFound`: mock returns NotFound, assert exit code 1 and error message
  - `TestPutCommand_Success`: mock accepts Put, assert success message
  - `TestPutCommand_WithTTL`: `--ttl 30s` flag sets ttl_seconds=30 in request
  - `TestDeleteCommand_GetsFirstThenDeletes`: assert Get is called before Delete
  - `TestStatusCommand_TextOutput`: mock Health + ClusterInfo, assert table format output
  - `TestStatusCommand_JSONOutput`: `--output json`, assert valid JSON output
  - `TestConfigFileLoaded`: write `~/.quorumctl.yaml` with address, assert used without --address flag
  - `TestTLSFlagsApplied`: `--tls-ca`, assert gRPC connection created with TLS credentials
- **Integration tests**:
  - `TestCLIAgainstLiveNode`: start single-node GoQuorum, run `quorumctl put foo bar`, `quorumctl get foo`, assert "bar" in output
  - `TestCLIStatusAgainstCluster`: 3-node cluster, run `quorumctl status`, assert 3 nodes listed

## 13. Open Questions

- Should `quorumctl` support an interactive REPL mode for exploratory use?
- Should we publish pre-built binaries as GitHub releases?
