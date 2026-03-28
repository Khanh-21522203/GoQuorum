---
name: Configuration
description: YAML configuration schema, defaults, validation, and preset builders for all subsystems
type: project
---

# Configuration

## Overview

All configuration lives in a single YAML file (default `config.yaml`). `config.LoadConfig(path)` (`internal/config/config.go`) reads, unmarshals, applies defaults, and validates. The resulting `Config` struct is passed to component constructors at startup.

## Top-Level Config Struct

```go
type Config struct {
    Node        NodeConfig
    Cluster     ClusterConfig
    Storage     StorageConfig
    Quorum      QuorumConfig
    ReadRepair  ReadRepairConfig
    AntiEntropy AntiEntropyConfig
    Connection  ConnectionConfig
    Server      ServerConfig
    Gossip      GossipConfig
}
```

## Node Config

```yaml
node:
  node_id:  "node1"       # Required; 1–64 alphanumeric/hyphen/underscore chars
  data_dir: "./data"      # Pebble DB directory; default ./data
  log_level: "info"       # debug/info/warn/error
```

## Cluster Config (`internal/config/cluster.go`)

```yaml
cluster:
  node_id: "node1"
  listen_addr: "0.0.0.0:7070"
  members:
    - id: "node1"
      addr: "node1:7070"
      http_addr: "node1:8080"
    - id: "node2"
      addr: "node2:7070"
      http_addr: "node2:8080"
  heartbeat_interval: "1s"
  heartbeat_timeout:  "2s"
  failure_threshold:  5
  bootstrap_timeout:  "60s"
```

**Validation invariants**:
- INV-M2: All member IDs must be unique.
- INV-M4: Self node (matching `node_id`) must be in `members`.
- Addresses: valid `host:port`, port 1024–65535.

`GetPeers()` returns all members except self. `QuorumSize()` returns `(len(Members)/2)+1`.

## Quorum Config (`internal/config/quorum.go`)

```yaml
quorum:
  n: 3       # Replication factor
  r: 2       # Read quorum
  w: 2       # Write quorum
  sloppy_quorum: true
```

Preset builders:
| Builder | N | R | W | Notes |
|---|---|---|---|---|
| `StrongConsistencyConfig()` | 3 | 2 | 2 | R+W=4 > N → read-write overlap |
| `EventualConsistencyConfig()` | 3 | 1 | 1 | Fastest; may read stale |
| `ReadOptimizedConfig()` | 3 | 1 | 3 | Fast reads, slow writes |
| `WriteOptimizedConfig()` | 3 | 3 | 1 | Fast writes, slow reads |
| `HighAvailabilityConfig()` | 5 | 2 | 2 | Tolerates 3 failures |

## Storage Config (`internal/config/storage.go`)

```yaml
storage:
  sync_writes: true
  cache_size_mb: 256
  memtable_mb: 64
  max_open_files: 1000
  max_key_size_kb: 64
  max_value_size_mb: 1
  max_siblings: 100
  sibling_warning_threshold: 5
  tombstone_gc_enabled: true
  tombstone_ttl_days: 7
  tombstone_gc_interval: "1h"
  vclock_prune_days: 7
  vclock_max_entries: 50
```

Converter functions provide typed byte values: `BlockCacheSize() = CacheSizeMB << 20`, `MemTableSize() = MemTableMB << 20`, etc.

## Read Repair Config (`internal/config/repair.go`)

```yaml
read_repair:
  enabled: true
  async: true         # Non-blocking repair
  timeout: "1s"
  probability: 1.0    # 0.0–1.0; reduce for low-priority keys
```

## Anti-Entropy Config

```yaml
anti_entropy:
  enabled: true
  scan_interval: "1h"
  exchange_timeout: "30s"
  max_bandwidth: 10485760   # bytes/sec = 10 MB/s
  parallelism: 1
  merkle_depth: 10           # 2^10 = 1024 buckets
```

`NumBuckets() = 2^merkle_depth`. Scheduler tick = `scan_interval / NumBuckets`.

## Connection Config (`internal/config/connection.go`)

```yaml
connection:
  pool_size: 10
  idle_timeout: "60s"
  max_lifetime: "1h"
  dial_timeout: "5s"
  reconnect_base: "100ms"
  reconnect_max: "30s"
  reconnect_factor: 2.0
  max_reconnect_attempts: 10
```

Exponential backoff: `base × factor^n`, capped at `reconnect_max`.

## Server Config

```yaml
server:
  grpc_addr: ":7070"
  http_addr: ":8080"
  tls:
    enabled: false
    cert_file: ""
    key_file: ""
    ca_file: ""
    mtls_enabled: false
  rate_limit:
    global_rps: 0      # 0 = unlimited
    per_ip_rps: 0
    burst_factor: 1.0
```

## Gossip Config

```yaml
gossip:
  enabled: true
  fan_out: 3       # Peers per gossip round
  interval: "1s"   # Round frequency
```

## Failure Detector Config

Merged into `ClusterConfig.HeartbeatInterval/Timeout/FailureThreshold`.
`FailureDetectorConfig` also has:
- `ReplicaRPCTimeout`: 2s
- `SlowNodeLatencyThreshold`: 1s
- `SlowNodeTimeoutThreshold`: 10%

## Defaults and Validation

`applyDefaults()` fills: `LogLevel="info"`, `DataDir="./data"`, `Quorum=DefaultQuorumConfig()`, `ReadRepair=DefaultReadRepairConfig()`, etc.

`Validate()` checks:
- Quorum: `N ≤ len(Cluster.Members)` (cannot replicate to more nodes than exist)
- All sub-config validations

`PrintSummary()` logs: node ID, cluster size, quorum settings, repair/anti-entropy status on startup.

## Example: Production 3-Node Config

```yaml
node:
  node_id: "node1"
  data_dir: "/var/lib/goquorum"
  log_level: "info"
cluster:
  node_id: "node1"
  listen_addr: "0.0.0.0:7070"
  members:
    - {id: node1, addr: "node1:7070", http_addr: "node1:8080"}
    - {id: node2, addr: "node2:7070", http_addr: "node2:8080"}
    - {id: node3, addr: "node3:7070", http_addr: "node3:8080"}
  failure_threshold: 5
  bootstrap_timeout: "60s"
quorum: {n: 3, r: 2, w: 2}
storage:
  sync_writes: true
  cache_size_mb: 256
```

Changes:

