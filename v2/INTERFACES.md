# GoQuorum v2 — Interface Spec

Authoritative cross-module contract. Signatures below are derived from the v1 codebase
and adapted to the v2 port/adapter split. Implement exactly these boundaries so modules
compose without drift. Internal (unexported) detail is left to each module's author.

Legend: `[PORT]` = interface declared in `engine`, implemented in `infra`.
All stubs return `ErrNotImplemented` this phase (see CONVENTIONS.md).

---

## Module: contracts  (`goquorum.io/v2/contracts`) — leaf, stdlib only

Packages and their key exported surface:

### `contracts` (root)
```go
var ErrNotImplemented = errors.New("not implemented")
```

### `contracts/node`
```go
type NodeID string
func (n NodeID) Validate() bool            // len 1..64, alnum + '-' '_'

type NodeState int
const ( NodeStateActive NodeState = iota; NodeStateFailed; NodeStateDegraded; NodeStateLeaving; NodeStateUnknown )
func (s NodeState) String() string

type Node struct {
	ID               NodeID
	Addr             string // host:port
	State            NodeState
	VirtualNodeCount int
	MissedHeartbeats int
	LastHeartbeat    time.Time
	// unexported mu guards mutable fields
}
func (n *Node) UpdateState(state NodeState)
func (n *Node) GetState() NodeState
func (n *Node) RecordHeartbeat()
func (n *Node) RecordMissedHeartbeat(threshold int)

type NodeHealth struct { NodeID NodeID; State NodeState; LastHeartbeat time.Time; MissedHeartbeats int; LastLatency time.Duration }
func (nh *NodeHealth) IsHealthy() bool
func (nh *NodeHealth) CanServeReads() bool
func (nh *NodeHealth) CanServeWrites() bool

type PeerStatus int
const ( PeerStatusUnknown PeerStatus = iota; PeerStatusActive; PeerStatusSuspect; PeerStatusFailed )
func (s PeerStatus) String() string

type PeerInfo struct { ID NodeID; Addr string; Status PeerStatus; LastSeen time.Time }
```

### `contracts/quorumerr`
```go
type QuorumErrorType int
const ( QuorumNotReached QuorumErrorType = iota; AllReplicasUnavailable )
func (t QuorumErrorType) String() string

type ReplicaError struct { NodeID node.NodeID; Addr string; Error error }

type QuorumError struct {
	Type          QuorumErrorType
	Required      int
	Achieved      int
	Operation     string // "read" | "write"
	ReplicaErrors []ReplicaError
}
func (e *QuorumError) Error() string
func (e *QuorumError) Details() map[string]interface{}

var (
	ErrKeyNotFound   = errors.New("key not found")
	ErrCorruptedData = errors.New("data corruption detected")
	ErrStorageClosed = errors.New("storage is closed")
	ErrStorageFull   = errors.New("storage full (disk space exhausted)")
	ErrStorageIO     = errors.New("storage I/O error")
)
```

### `contracts/vclock`  (fix v1 footgun: value copies must NOT share the map — Copy() deep-copies)
```go
type Ordering int
const ( Before Ordering = iota; After; Equal; Concurrent )
func (o Ordering) String() string

type VectorClock struct { /* unexported entries map[node.NodeID]*entry */ }
func NewVectorClock() VectorClock
func (vc *VectorClock) Tick(id node.NodeID)
func (vc VectorClock) Get(id node.NodeID) uint64
func (vc *VectorClock) Set(id node.NodeID, counter uint64)
func (vc *VectorClock) Merge(other VectorClock)          // max per node
func (vc VectorClock) Copy() VectorClock                 // deep copy
func (vc VectorClock) IsEmpty() bool
func (vc VectorClock) Size() int
func (vc VectorClock) Compare(other VectorClock) Ordering
func (vc VectorClock) HappensBefore(other VectorClock) bool
func (vc VectorClock) HappensAfter(other VectorClock) bool
func (vc VectorClock) IsConcurrentWith(other VectorClock) bool
func (vc VectorClock) Equals(other VectorClock) bool
func (vc VectorClock) Dominates(other VectorClock) bool
func (vc *VectorClock) Prune(threshold time.Duration, maxEntries int) int
func (vc VectorClock) MarshalBinary() ([]byte, error)
func (vc *VectorClock) UnmarshalBinary(data []byte) error
func (vc VectorClock) MarshalJSON() ([]byte, error)
func (vc *VectorClock) UnmarshalJSON(data []byte) error
```

### `contracts/wire`  (proto-generated types placeholder)
- Hand-written stub types mirroring the KV API request/response shapes for now.
- `// TODO(v2): replace with buf-generated code (proto/ + buf.gen.yaml)`.

---

## Module: engine  (`goquorum.io/v2/engine`) — pure domain + PORTS; imports only contracts + stdlib

### `engine/storage` — value types + `[PORT]` Storage
```go
type Sibling struct { Value []byte; VClock vclock.VectorClock; Timestamp int64; Tombstone bool; ExpiresAt int64 }
type SiblingSet struct { Siblings []Sibling }
type ScanFunc func(key []byte, siblings *SiblingSet) bool
type Stats struct { KeyCount int64; SizeBytes uint64; L0FileCount int64; CompactionCount int64; WALBytesWritten uint64 }

// [PORT] implemented by infra/storage/pebble
type Storage interface {
	Get(key []byte) (*SiblingSet, error)                     // filters tombstones + expired
	GetRaw(key []byte) (*SiblingSet, error)                  // tombstones visible
	Put(key []byte, siblings *SiblingSet) error
	Delete(key []byte, ctx vclock.VectorClock) error         // writes tombstone
	Scan(start, end []byte, fn ScanFunc) error
	LocalNodeID() node.NodeID
	Stats() Stats
	Close() error
}
```

### `engine/transport` — `[PORT]` Transport (v1 `RPCClient`, honestly named)
```go
// [PORT] implemented by infra/transport
type Transport interface {
	RemotePut(ctx context.Context, id node.NodeID, key []byte, siblings *storage.SiblingSet) error
	RemoteGet(ctx context.Context, id node.NodeID, key []byte) (*storage.SiblingSet, error)
	Heartbeat(ctx context.Context, id node.NodeID) error
	GetMerkleRoot(ctx context.Context, id node.NodeID) ([]byte, error)
	NotifyLeaving(ctx context.Context, id node.NodeID) error
	Close() error
}
```

### `engine/hashring`
```go
type HashRing struct { /* ... */ }
func NewHashRing(vnodeCount int) *HashRing   // default 256 if <= 0
func (hr *HashRing) AddNode(n *node.Node) error
func (hr *HashRing) RemoveNode(id node.NodeID) error
func (hr *HashRing) GetPreferenceList(key string, n int) ([]node.NodeID, error)
func (hr *HashRing) GetPrimaryNode(key string) (node.NodeID, error)
func (hr *HashRing) Nodes() []*node.Node
func (hr *HashRing) Size() int
```

### `engine/membership`
```go
type NodeStatus int
const ( NodeStatusUnknown NodeStatus = iota; NodeStatusJoining; NodeStatusActive; NodeStatusSuspect; NodeStatusFailed; NodeStatusLeaving )
func (s NodeStatus) String() string
type MembershipManager struct { /* ... */ }
func NewMembershipManager(/* cfg passed as engine-local config type */) *MembershipManager
func (mm *MembershipManager) GetActivePeers() []node.NodeID
func (mm *MembershipManager) HasQuorum() bool
func (mm *MembershipManager) GetPeerAddr(id node.NodeID) (string, bool)
func (mm *MembershipManager) LocalNodeID() node.NodeID
// (carry the remaining v1 methods as stubs)
```

### `engine/gossip`, `engine/failuredetector`, `engine/antientropy`, `engine/handoff`, `engine/readrepair`
- Mirror v1 exported surface (see v1 `internal/cluster/*`), with two substitutions:
  concrete `*storage.Storage` → `storage.Storage` (the port); `RPCClient` → `transport.Transport`.
- `antientropy` contains both `AntiEntropy` and `MerkleTree` (+ `BucketRange`).
- `failuredetector` keeps exported callbacks `OnNodeRecovery`, `OnNodeFailed`.

### `engine/coordinator` — the quorum orchestrator
```go
type PutOptions struct { TTLSeconds int64 }
type Coordinator struct { /* depends on storage.Storage + transport.Transport ports */ }
func NewCoordinator(id node.NodeID, ring *hashring.HashRing, store storage.Storage,
	tr transport.Transport, mm *membership.MembershipManager /*, cfg */) *Coordinator
func (c *Coordinator) Start() error
func (c *Coordinator) Stop()
func (c *Coordinator) Put(ctx context.Context, key string, value []byte, causal vclock.VectorClock, opts ...PutOptions) (vclock.VectorClock, error)
func (c *Coordinator) Get(ctx context.Context, key string) ([]storage.Sibling, error)
func (c *Coordinator) Delete(ctx context.Context, key string, causal vclock.VectorClock) error
func (c *Coordinator) GetMerkleRoot() []byte
```

### `engine/config` (engine-local config value types the ports/domain need)
- Plain structs: `QuorumConfig{N,R,W int; SloppyQuorum bool}`, `ReadRepairConfig`, `AntiEntropyConfig`,
  `FailureDetectorConfig`, `TimeoutConfig`. NO yaml tags here (loading lives in infra/config).

---

## Module: infra  (`goquorum.io/v2/infra`) — adapters; may import external deps ONLY in real impl (not scaffold)

- `infra/storage/pebble` — `type Store struct{}` implements `engine/storage.Storage`.
  `func NewStore(opts Options) (*Store, error)`. `// TODO(v2): import github.com/cockroachdb/pebble`.
- `infra/transport/httprpc` — implements `engine/transport.Transport` over HTTP/JSON (v1 reality).
  `// TODO(v2): import net/http`.
- `infra/config` — YAML loader producing engine config value types. **All structs get yaml tags.**
  `func Load(path string) (*Config, error)`. `// TODO(v2): import gopkg.in/yaml.v3`.
- `infra/observability` — logging + Prometheus metrics registry. `// TODO(v2): import prometheus`.
- `infra/security` — TLS config assembly.
- `infra/backup` — Pebble checkpoint backup/restore.

---

## Module: gateway  (`goquorum.io/v2/gateway`)
- `gateway/http` — HTTP/JSON gateway translating REST → coordinator calls (v1 used grpc-gateway).
  Stub `type Gateway struct{}`, `func New(...) *Gateway`, `func (g *Gateway) Handler() http.Handler`
  returning a not-implemented handler. `// TODO(v2): import grpc-gateway`.

---

## Module: server  (`goquorum.io/v2/server`) — service impls + composition
- `server/api` — `ClientAPI`, `AdminAPI`, `InternalAPI` (KV + admin + node-to-node), mirroring v1
  `internal/server/*_api.go` signatures, but consuming `*coordinator.Coordinator` and the ports.
- `server/app` — `type Server struct{}`, `func New(cfg) (*Server, error)`, `Start()/Stop()`; wires
  concrete `infra` adapters into `engine` and mounts `gateway`.

---

## Module: client  (`goquorum.io/v2/client`)
```go
type Sibling struct { Value []byte; Context vclock.VectorClock; Timestamp int64; Tombstone bool }
type ClientConfig struct { Addr string; DialTimeout, RequestTimeout, RetryBaseDelay time.Duration; MaxRetries int }
func DefaultClientConfig(addr string) ClientConfig
type Client struct { /* ... */ }
func NewClient(cfg ClientConfig) (*Client, error)
func (c *Client) Get(ctx context.Context, key []byte) ([]Sibling, error)
func (c *Client) Put(ctx context.Context, key, value []byte, causal vclock.VectorClock) (vclock.VectorClock, error)
func (c *Client) Delete(ctx context.Context, key []byte, causal vclock.VectorClock) error
func (c *Client) Close() error
type ConflictResolver interface { Resolve(siblings []Sibling) ([]byte, vclock.VectorClock, error) }
type LWWResolver struct{}
```

---

## Module: cli  (`goquorum.io/v2/cli`)
- `cli/cmd/quorum/main.go` — server daemon; flag `--config`; boot sequence wiring
  infra + engine + server (mirror v1 cmd/quorum boot order as comments/stubs).
- `cli/cmd/quorumctl/main.go` — control CLI; flag `--addr`; subcommands
  get/put/delete/status/ring/key-info/compact.
- `cli/internal/command` — subcommand implementations (stubs).

---

## Module: test  (`goquorum.io/v2/test`)
- `test/harness` — helpers to spin up an in-process node/cluster for tests (stub).
- `test/integration` — table-driven integration test skeletons (build-tagged or `t.Skip`
  with `ErrNotImplemented` so `go test ./...` is green).
- `test/benchmarks` — benchmark harness skeleton.
