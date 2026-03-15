# Feature: Configuration Management

## 1. Purpose

The configuration subsystem loads, validates, and provides all tuning parameters to GoQuorum components at startup. It reads from a YAML file and allows overriding any value via environment variables. Well-validated configuration prevents silent misconfiguration (e.g., `R + W ≤ N` would violate consistency guarantees) and provides sensible defaults so a single-node development setup works out of the box with no manual configuration.

## 2. Responsibilities

- **YAML parsing**: Read `config.yaml` from a configurable path; parse into a typed `Config` struct using `gopkg.in/yaml.v3`
- **Environment overrides**: Allow any config value to be overridden by an environment variable (e.g., `GOQUORUM_NODE_ID` overrides `node.id`)
- **Validation**: Enforce consistency constraints at startup: `R + W > N`, `1 ≤ W ≤ N`, `1 ≤ R ≤ N`, positive timeouts, valid addresses
- **Defaults**: Provide production-ready defaults for all fields so minimal configuration is needed
- **Profile support**: Ship example configuration files for common profiles: single-node dev, 3-node cluster, high-availability (N=5)
- **Config reload**: Support selective reload of logging level and rate limits on SIGHUP without full restart

## 3. Non-Responsibilities

- Does not implement runtime configuration changes beyond log level and rate limits
- Does not implement configuration service (etcd, Consul) integration
- Does not validate certificate file contents (only checks file existence)
- Does not implement config versioning or migration

## 4. Architecture Design

```
main.go:
  cfg = config.Load(flagConfigPath)
  │
  ├── config.ParseYAML(path) → raw Config
  ├── config.ApplyEnvOverrides(&cfg)
  └── config.Validate(cfg) → error

Config struct:
  Node:        NodeConfig       (id, address, grpc_port, http_port)
  Cluster:     ClusterConfig    (peers, N, R, W, heartbeat, failure thresholds)
  Storage:     StorageConfig    (data_dir, pebble tuning, GC, tombstone TTL)
  AntiEntropy: AntiEntropyConfig
  ReadRepair:  ReadRepairConfig
  Observability: ObservabilityConfig
  TLS:         TLSConfig
  RateLimit:   RateLimitConfig
  Backup:      BackupConfig
  Batch:       BatchConfig
```

## 5. Core Data Structures (Go)

```go
package config

import (
    "time"
    "os"
    "gopkg.in/yaml.v3"
)

// Config is the root configuration struct for a GoQuorum node.
type Config struct {
    Node          NodeConfig          `yaml:"node"`
    Cluster       ClusterConfig       `yaml:"cluster"`
    Storage       StorageConfig       `yaml:"storage"`
    AntiEntropy   AntiEntropyConfig   `yaml:"anti_entropy"`
    ReadRepair    ReadRepairConfig    `yaml:"read_repair"`
    Observability ObservabilityConfig `yaml:"observability"`
    TLS           TLSConfig           `yaml:"tls"`
    RateLimit     RateLimitConfig     `yaml:"rate_limit"`
    Backup        BackupConfig        `yaml:"backup"`
    Batch         BatchConfig         `yaml:"batch"`
}

// NodeConfig identifies this node and its network addresses.
type NodeConfig struct {
    ID       string `yaml:"id"`      // unique node identifier; defaults to hostname
    GRPCAddr string `yaml:"grpc_addr" default:":7070"`
    HTTPAddr string `yaml:"http_addr" default:":8080"`
}

// ClusterConfig defines quorum parameters and cluster topology.
type ClusterConfig struct {
    N     int      `yaml:"n" default:"3"`
    R     int      `yaml:"r" default:"2"`
    W     int      `yaml:"w" default:"2"`
    Peers []string `yaml:"peers"` // gRPC addresses of other nodes

    VirtualNodes         int           `yaml:"virtual_nodes" default:"256"`
    HeartbeatInterval    time.Duration `yaml:"heartbeat_interval" default:"1s"`
    HeartbeatTimeout     time.Duration `yaml:"heartbeat_timeout" default:"500ms"`
    SuspectThreshold     int           `yaml:"suspect_threshold" default:"3"`
    FailureThreshold     int           `yaml:"failure_threshold" default:"5"`
    ReplicateTimeout     time.Duration `yaml:"replicate_timeout" default:"5s"`
    ReadTimeout          time.Duration `yaml:"read_timeout" default:"5s"`

    SloppyQuorum SloppyQuorumConfig `yaml:"sloppy_quorum"`
    HintedHandoff HintedHandoffConfig `yaml:"hinted_handoff"`
    Gossip       GossipConfig        `yaml:"gossip"`
}

// StorageConfig controls the Pebble storage engine.
type StorageConfig struct {
    DataDir         string        `yaml:"data_dir" default:"./data"`
    SyncWrites      bool          `yaml:"sync_writes" default:"true"`
    CacheSizeMB     int           `yaml:"cache_size_mb" default:"256"`
    MemtableSizeMB  int           `yaml:"memtable_size_mb" default:"64"`
    MaxKeySizeBytes int           `yaml:"max_key_size_bytes" default:"65536"`
    MaxValueSizeMB  int           `yaml:"max_value_size_mb" default:"1"`
    MaxSiblings     int           `yaml:"max_siblings" default:"100"`
    TombstoneTTL    time.Duration `yaml:"tombstone_ttl" default:"168h"`
    GCInterval      time.Duration `yaml:"gc_interval" default:"1h"`

    VClockMaxEntries int           `yaml:"vclock_max_entries" default:"50"`
    VClockMaxAge     time.Duration `yaml:"vclock_max_age" default:"0"`
}
```

## 6. Public Interfaces

```go
package config

// Load reads and validates the configuration file at path.
// Applies environment variable overrides before validation.
// Returns a fully populated Config with defaults applied.
func Load(path string) (*Config, error)

// LoadFromBytes parses config from raw YAML bytes. Used in tests.
func LoadFromBytes(data []byte) (*Config, error)

// Validate checks all inter-field constraints.
// Returns a descriptive error for the first violation found.
func Validate(cfg *Config) error

// ApplyDefaults fills in zero-value fields with their documented defaults.
func ApplyDefaults(cfg *Config)

// ApplyEnvOverrides applies environment variable overrides.
// Convention: GOQUORUM_<SECTION>_<FIELD> (e.g., GOQUORUM_CLUSTER_N=5).
func ApplyEnvOverrides(cfg *Config)
```

## 7. Internal Algorithms

### Load
```
Load(path):
  data, err = os.ReadFile(path)
  if err != nil: return nil, fmt.Errorf("read config: %w", err)

  cfg = &Config{}
  if err = yaml.Unmarshal(data, cfg); err != nil:
    return nil, fmt.Errorf("parse config: %w", err)

  ApplyDefaults(cfg)
  ApplyEnvOverrides(cfg)

  if err = Validate(cfg); err != nil:
    return nil, fmt.Errorf("invalid config: %w", err)

  // Auto-set node ID from hostname if not configured
  if cfg.Node.ID == "":
    cfg.Node.ID, _ = os.Hostname()

  return cfg, nil
```

### Validate (key checks)
```
Validate(cfg):
  // Quorum consistency constraint
  if cfg.Cluster.R + cfg.Cluster.W <= cfg.Cluster.N:
    return error("R(%d) + W(%d) must be > N(%d) for consistent reads", R, W, N)

  // Quorum bounds
  if cfg.Cluster.W < 1 || cfg.Cluster.W > cfg.Cluster.N:
    return error("W must be in [1, N]")
  if cfg.Cluster.R < 1 || cfg.Cluster.R > cfg.Cluster.N:
    return error("R must be in [1, N]")

  // Timing sanity
  if cfg.Cluster.HeartbeatInterval <= 0:
    return error("heartbeat_interval must be positive")
  if cfg.Cluster.HeartbeatTimeout >= cfg.Cluster.HeartbeatInterval:
    return error("heartbeat_timeout must be < heartbeat_interval")

  // Storage
  if cfg.Storage.DataDir == "":
    return error("storage.data_dir is required")
  if cfg.Storage.TombstoneTTL < cfg.Cluster.HeartbeatInterval * time.Duration(cfg.Cluster.FailureThreshold) * 10:
    log.Warn("tombstone_ttl may be too short relative to failure detection threshold")

  // Peers: for N>1, at least N-1 peers must be configured
  if cfg.Cluster.N > 1 && len(cfg.Cluster.Peers) < cfg.Cluster.N-1:
    return error("need at least N-1=%d peers configured for N=%d", N-1, N)

  // TLS: if enabled, cert and key files must exist
  if cfg.TLS.Enabled:
    for _, path in [cfg.TLS.ServerCert, cfg.TLS.ServerKey]:
      if _, err = os.Stat(path); err != nil:
        return error("tls cert/key file not found: %s", path)

  return nil
```

### ApplyEnvOverrides
```
ApplyEnvOverrides(cfg):
  // Map GOQUORUM_CLUSTER_N → cfg.Cluster.N
  // Uses reflection or a flat env-var map for each field.
  if v = os.Getenv("GOQUORUM_NODE_ID"); v != "":
    cfg.Node.ID = v
  if v = os.Getenv("GOQUORUM_NODE_GRPC_ADDR"); v != "":
    cfg.Node.GRPCAddr = v
  if v = os.Getenv("GOQUORUM_CLUSTER_N"); v != "":
    cfg.Cluster.N, _ = strconv.Atoi(v)
  // ... all fields ...
```

## 8. Persistence Model

Configuration is read from disk at startup only. There is no runtime persistence or dynamic update (except log level and rate limit reloads on SIGHUP). If configuration changes are needed, restart the node.

### Shipped Configuration Profiles

| File | Profile | N/R/W | Use Case |
|------|---------|-------|----------|
| `config/single-node.yaml` | Development | 1/1/1 | Single process, no replication |
| `config/3-node-cluster.yaml` | Standard | 3/2/2 | Balanced consistency/availability |
| `config/5-node-ha.yaml` | High Availability | 5/3/3 | Tolerates 2 node failures |

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Read-only after startup | `Config` is loaded once and passed as read-only to all components |
| No locks needed | Immutable after `Load()` returns |
| SIGHUP partial reload | Only mutable fields (log level, rate limits) are updated on reload; requires component-level lock in those subsystems |

## 10. Configuration

The `config` package is itself the configuration layer; it has no configuration of its own.

## 11. Observability

- Log at INFO at startup: all resolved configuration values (redacting TLS key paths)
- Log at WARN for any soft constraint violations (e.g., tombstone TTL shorter than recommended)
- Log at INFO when SIGHUP triggers a partial reload, listing which fields changed

## 12. Testing Strategy

- **Unit tests**:
  - `TestLoadValidConfig`: valid YAML, assert parsed struct equals expected
  - `TestLoadMissingFile`: nonexistent path, assert error
  - `TestLoadInvalidYAML`: malformed YAML, assert parse error
  - `TestValidateQuorumConstraint`: R+W <= N, assert error
  - `TestValidateWBounds`: W=0, assert error; W=N+1, assert error
  - `TestValidateTimingConstraints`: heartbeat_timeout >= heartbeat_interval, assert error
  - `TestValidatePeerCount`: N=3, only 1 peer, assert error
  - `TestValidateTLSFiles`: TLS enabled, cert file missing, assert error
  - `TestApplyDefaults`: empty config, assert all defaults applied correctly
  - `TestEnvOverride`: set GOQUORUM_CLUSTER_N=5, assert cfg.Cluster.N=5
  - `TestAutoNodeID`: node.id empty, assert set to hostname
- **Integration tests**:
  - `TestLoadAndStartNode`: load single-node config, start GoQuorum node, assert healthy

## 13. Open Questions

None.
