---
name: CLI Tool (quorumctl)
description: Command-line administrative client for data operations and cluster inspection
type: project
---

# CLI Tool (quorumctl)

## Overview

`quorumctl` (`cmd/quorumctl/main.go`) is a command-line client for GoQuorum. It connects to a running node via gRPC and provides data operations, cluster inspection, and administrative commands.

## Build & Run

```bash
make build        # produces bin/quorumctl (and bin/goquorum)
./bin/quorumctl --addr localhost:7070 <command> [args]
```

Default address: `localhost:7070`.

## Global Flags

| Flag | Default | Description |
|---|---|---|
| `--addr` | `localhost:7070` | gRPC server address |

All commands use a 10-second timeout.

## Commands

### `get <key>`

Fetches a key and displays all sibling versions:

```
$ quorumctl get mykey
Found 1 sibling(s):
  [0] value="hello" timestamp=1711537200 tombstone=false
       context: {"node1":3,"node2":2}
```

- Calls `GoQuorum.Get` (R quorum = 0, uses server default).
- Shows each sibling's value (as string), timestamp, tombstone flag.
- Shows the vector clock context as JSON (for use in subsequent writes).
- Multiple siblings indicate an unresolved conflict.

### `put <key> <value>`

Writes a value with a blind write (empty context):

```
$ quorumctl put mykey "hello world"
OK, new context: eyJub2RlMSI6M30=   (base64-encoded JSON vector clock)
```

- Calls `GoQuorum.Put` with no prior context (W quorum = 0, server default).
- Returns the new vector clock context encoded as `base64(JSON)`.
- Pass this context back on the next write to avoid creating siblings.

### `delete <key> <context>`

Deletes a key using a vector clock context from a prior `get`:

```
$ quorumctl delete mykey eyJub2RlMSI6M30=
Deleted.
```

- `<context>` is the base64-encoded JSON vector clock from `get` or `put` output.
- Calls `GoQuorum.Delete`.

### `status`

Displays health and uptime:

```
$ quorumctl status
Node:    node1
Version: 0.1.0-mvp
Uptime:  3h42m15s
Status:  healthy
Checks:
  storage: healthy (0.3ms)
  cluster: healthy (2/2 peers active)
  disk:    healthy (85 GB free of 100 GB)
```

- Calls `GoQuorumAdmin.Health`.

### `ring`

Displays cluster topology and node health:

```
$ quorumctl ring
Cluster Ring (3 nodes):
  node1  node1:7070  Active   latency=1.2ms  last-seen=2s ago
  node2  node2:7070  Active   latency=0.9ms  last-seen=1s ago
  node3  node3:7070  Suspect  latency=45ms   last-seen=8s ago
```

- Calls `GoQuorumAdmin.ClusterInfo` (or `GET /debug/cluster`).

### `key-info <key>`

Shows which replicas own a key and their current sibling state:

```
$ quorumctl key-info mykey
Key: mykey
Preference list: [node1, node2, node3]
Replicas:
  node1: 1 sibling(s), ts=1711537200, size=11B, tombstone=false
  node2: 1 sibling(s), ts=1711537200, size=11B, tombstone=false
  node3: MISSING (node suspect)
```

- Calls `GoQuorumAdmin.KeyInfo`.

### `compact`

Triggers a manual Pebble LSM compaction:

```
$ quorumctl compact
Compaction started.
```

- Calls `GoQuorumAdmin.TriggerCompaction`.
- Returns immediately; compaction runs asynchronously in the background.

## Vector Clock Encoding

The CLI encodes vector clocks as `base64(JSON)` for human passability:
- JSON form: `{"node1": 3, "node2": 2}` (compact, counter-only).
- Base64: `eyJub2RlMSI6Mywibm9kZTIiOjJ9`.

On `put`, the returned context is the new clock. On `delete`, the context must come from a prior `get` or `put` output.

## gRPC Clients

`quorumctl` instantiates two gRPC clients:
- `GoQuorumClient` — data plane (`Get`, `Put`, `Delete`, `BatchGet`, `BatchPut`).
- `GoQuorumAdminClient` — admin plane (`Health`, `ClusterInfo`, `KeyInfo`, `TriggerCompaction`).

Both use insecure credentials by default. TLS flag support is not yet implemented in the CLI.

Changes:

