# GoQuorum — Architecture Diagrams

Mermaid diagrams at every C4 level, plus sequence and state diagrams.

---

## 1. C4 Level 1 — System Context

Who interacts with GoQuorum and what external systems it touches.

```mermaid
flowchart TB
    subgraph boundary["GoQuorum Cluster"]
        GQ[("⚙️ GoQuorum\nDynamo-style KV Store")]
    end

    Client[("👤 Application Client\nGo SDK / quorumctl CLI")]
    Prom[("📊 Prometheus\nMetrics Scraper")]
    Loki[("📝 Loki\nLog Aggregator")]
    Grafana[("📈 Grafana\nDashboards")]

    Client  -->|"gRPC :7070\nGet / Put / Delete / Batch"| GQ
    Prom    -->|"HTTP GET /metrics"| GQ
    GQ      -->|"Structured logs"| Loki
    Loki    -->|"Log datasource"| Grafana
    Prom    -->|"Metrics datasource"| Grafana
```

---

## 2. C4 Level 2 — Container Diagram

Deployable units inside a single GoQuorum node.

```mermaid
flowchart TB
    Client[("👤 Application Client")]
    PeerNode[("🖥️ Peer GoQuorum Node")]

    subgraph node["GoQuorum Node (single process)"]
        direction TB

        subgraph apis["API Layer (:7070 gRPC + :8080 HTTP)"]
            GRPC["gRPC Server\nClient API + Admin API"]
            HTTP["HTTP/JSON Gateway\nInternal Node RPC"]
        end

        subgraph core["Core Engine"]
            COORD["Coordinator\nQuorum orchestration"]
            CLUSTER["Cluster Subsystem\nMembership · Gossip · FailureDetector\nHashRing · AntiEntropy · ReadRepair\nHintedHandoff · TTLSweeper"]
            VCLOCK["Vector Clock\nCausality tracking"]
        end

        subgraph persistence["Persistence"]
            STORAGE["Storage Engine\nPebble LSM abstraction"]
            PEBBLE[("🗄️ Pebble DB\nCockroachDB LSM\ndata_dir/node<id>")]
        end

        subgraph obs["Observability"]
            METRICS["Prometheus Metrics\n/metrics endpoint"]
            LOGGER["Structured Logger\nLoki-compatible JSON"]
        end
    end

    Client    -->|"gRPC"| GRPC
    GRPC      --> COORD
    HTTP      --> COORD
    COORD     --> CLUSTER
    COORD     --> VCLOCK
    COORD     --> STORAGE
    STORAGE   --> PEBBLE
    CLUSTER   -->|"HTTP/JSON :8080"| PeerNode
    CLUSTER   --> METRICS
    COORD     --> LOGGER
```

---

## 3. C4 Level 3 — Component Diagram

Internal components and their wiring inside the GoQuorum node process.

```mermaid
flowchart TB
    subgraph server["server/"]
        ClientAPI["ClientAPI\nGet·Put·Delete·Batch"]
        AdminAPI["AdminAPI\nHealth·ClusterInfo·Metrics·KeyInfo"]
        InternalAPI["InternalAPI\nReplicate·Read·MerkleRoot·Gossip"]
    end

    subgraph cluster["cluster/"]
        COORD["Coordinator\ncoordinator.go"]
        RING["HashRing\nhashring.go"]
        MEMBER["MembershipManager\nmembership.go"]
        FD["FailureDetector\nfailure_detector.go"]
        GOSSIP["Gossip\ngossip.go"]
        AE["AntiEntropy\nanti_entropy.go"]
        RR["ReadRepairer\nread_repair.go"]
        HH["HintedHandoff\nhinted_handoff.go"]
        TTL["TTLSweeper\nttl_sweeper.go"]
        RPC["RPCClient\nrpc_client.go (HTTP/JSON)"]
        MERKLE["MerkleTree\nmerkle_tree.go"]
        BOOT["Bootstrap\nbootstrap.go"]
    end

    subgraph storage["storage/"]
        ENG["Storage\nengine.go"]
        PEB["Pebble\npebble.go"]
        ENC["Encoding\nencoding.go"]
    end

    subgraph vclock["vclock/"]
        VC["VectorClock\nvclock.go"]
        CMP["Compare\ncompare.go"]
    end

    ClientAPI   --> COORD
    AdminAPI    --> COORD
    AdminAPI    --> MEMBER
    InternalAPI --> COORD
    InternalAPI --> MERKLE
    InternalAPI --> GOSSIP

    COORD --> RING
    COORD --> MEMBER
    COORD --> RPC
    COORD --> ENG
    COORD --> VC
    COORD --> RR
    COORD --> HH
    COORD --> AE

    AE --> MERKLE
    AE --> RPC
    AE --> ENG

    RR --> RPC
    RR --> ENG

    HH --> RPC
    HH --> MEMBER

    FD --> MEMBER
    FD -->|"OnNodeFailed"| GOSSIP
    FD -->|"OnNodeRecovery"| COORD

    GOSSIP --> MEMBER
    GOSSIP --> RPC

    TTL --> ENG
    TTL --> MERKLE

    ENG --> PEB
    ENG --> ENC
    VC  --> CMP

    BOOT --> FD
    BOOT --> MEMBER
```

---

## 4. Sequence — Client Write (Put) with Quorum

Full path for a `Put` call from client to quorum acknowledgement.

```mermaid
sequenceDiagram
    participant C  as Client
    participant S  as gRPC Server
    participant CO as Coordinator
    participant R1 as Replica 1 (local)
    participant R2 as Replica 2 (remote)
    participant R3 as Replica 3 (remote)
    participant HH as HintedHandoff

    C  ->>  S:  Put(key, value, vclock?)
    S  ->>  CO: Put(key, value, vclock)

    note over CO: Lock key (per-key sync.Mutex)\nTick local VectorClock\nGet preference list (N nodes via HashRing)

    par Fan-out to N replicas (parallel goroutines)
        CO ->>  R1: localWrite(key, siblings)
        CO ->>+ R2: HTTP POST /internal/replicate
        CO ->>+ R3: HTTP POST /internal/replicate
    end

    R1 -->> CO: ok
    R2 -->> CO: ok
    R3 --x  CO: timeout / error

    note over CO: successCount=2 >= W=2 → quorum reached

    CO ->>  HH: StoreHint(R3, key, siblings)

    note over CO: Async: antiEntropy.OnKeyUpdate(key)

    CO -->> S:  new VectorClock
    S  -->> C:  Put response {vclock}
```

---

## 5. Sequence — Client Read (Get) with Read Repair

Full path for a `Get` including sibling merging and probabilistic read repair.

```mermaid
sequenceDiagram
    participant C  as Client
    participant S  as gRPC Server
    participant CO as Coordinator
    participant R1 as Replica 1
    participant R2 as Replica 2 (stale)
    participant R3 as Replica 3
    participant RR as ReadRepairer

    C  ->>  S:  Get(key)
    S  ->>  CO: Get(key)

    note over CO: Get preference list (N nodes)\nFan-out reads to all N replicas

    par Parallel reads
        CO ->>  R1: localRead(key)
        CO ->>+ R2: HTTP POST /internal/read
        CO ->>+ R3: HTTP POST /internal/read
    end

    R1 -->> CO: SiblingSet {vc: {A:3, B:2}}
    R2 -->> CO: SiblingSet {vc: {A:2}}      ← stale
    R3 -->> CO: SiblingSet {vc: {A:3, B:2}}

    note over CO: successCount=3 >= R=2 → quorum reached\nMerge siblings: take union, remove dominated versions\nResult: {vc: {A:3, B:2}}

    CO ->>  RR: TriggerRepair(key, mergedSiblings, [R2])

    note over RR: Async (fire-and-forget)\nCheck: R2.vclock < merged.vclock → stale

    RR ->>  R2: HTTP POST /internal/replicate (repair)
    R2 -->> RR: ok

    CO -->> S:  SiblingSet (tombstones filtered)
    S  -->> C:  Get response {values, vclock}
```

---

## 6. Sequence — Anti-Entropy Merkle Sync

Background divergence detection and repair between two nodes.

```mermaid
sequenceDiagram
    participant AE as AntiEntropy (Node A)
    participant B  as Node B (peer)
    participant ST as Storage (Node A)

    note over AE: schedulerLoop ticks\nPick peer B, pick bucket range

    AE ->>+ B:  GET /internal/merkle-root {bucket_range}
    B  -->>- AE: root_hash_B

    note over AE: Compare root_hash_A vs root_hash_B

    alt Roots match
        note over AE: No divergence — skip
    else Roots differ
        note over AE: Walk Merkle tree: narrow to differing buckets

        AE ->>+ B:  POST /internal/merkle-exchange {subtree_range}
        B  -->>- AE: {keys: [...], siblings: [...]}

        loop For each divergent key
            AE ->>  ST: Get(key)
            ST -->> AE: localSiblings
            note over AE: Merge remote + local siblings\n(VectorClock max-merge)
            AE ->>  ST: Put(key, mergedSiblings)
        end
    end
```

---

## 7. Sequence — Node Recovery via Hinted Handoff

A previously failed node comes back online and receives buffered writes.

```mermaid
sequenceDiagram
    participant FD as FailureDetector
    participant HH as HintedHandoff
    participant CO as Coordinator
    participant RN as Recovered Node

    note over FD: heartbeatLoop detects\nRN responds after being FAILED

    FD ->>  CO: OnNodeRecovery(RN)
    FD ->>  CO: TriggerAntiEntropyWith(RN)

    note over HH: replayLoop fires (30s interval)\nDetects RN is now ACTIVE

    loop For each buffered hint for RN
        HH ->>+ RN: HTTP POST /internal/replicate {key, siblings}
        RN -->>- HH: ok
        note over HH: Discard hint on success
    end
```

---

## 8. State Diagram — Node Lifecycle

State transitions a peer node goes through from the perspective of a monitoring node.

```mermaid
stateDiagram-v2
    [*] --> Bootstrapping : node starts

    Bootstrapping --> Active : quorum reached\n(>= N/2+1 peers respond)
    Bootstrapping --> Bootstrapping : waiting for peers

    Active --> Suspect : 1 missed heartbeat

    Suspect --> Active : heartbeat recovered
    Suspect --> Failed : threshold missed heartbeats

    Failed --> Active : first heartbeat success\n→ OnNodeRecovery callback\n→ HintedHandoff replay\n→ AntiEntropy triggered

    Active --> Leaving : graceful shutdown signal
    Leaving --> [*] : NotifyLeaving RPC sent\nring rebalanced
```

---

## 9. Flowchart — Consistent Hashing Key Lookup

How a key maps to its N replica nodes via the virtual-node ring.

```mermaid
flowchart TD
    A["Client: Put(key, value)"] --> B["xxHash64(key) → keyHash"]
    B --> C["Binary search vnodes\n(sorted ring of 256 × physicalNodes)"]
    C --> D["Find first vnode ≥ keyHash\n(wrap-around if needed)"]
    D --> E["Walk clockwise collecting\nN distinct physical nodes"]
    E --> F{"Reached N distinct\nphysical nodes?"}
    F -->|No| G["Next vnode on ring"]
    G --> F
    F -->|Yes| H["Preference List: [N1, N2, N3]"]
    H --> I{"SloppyQuorum\nenabled?"}
    I -->|Yes + node down| J["Append overflow nodes\nuntil W responses possible"]
    I -->|No| K["Use preference list as-is"]
    J --> K
    K --> L["Fan-out reads/writes\nto all N replicas in parallel"]
```

---

## 10. Flowchart — Write Quorum Evaluation

Decision logic inside `Coordinator.Put` after fan-out.

```mermaid
flowchart TD
    A["Fan-out complete\n(all N goroutines returned)"] --> B["Count successes"]
    B --> C{"successes >= W?"}
    C -->|Yes| D["Quorum reached ✓"]
    C -->|No| E{"SloppyQuorum\nenabled?"}
    E -->|No| F["Return QuorumError\n(write failed)"]
    E -->|Yes| G["Try overflow nodes\nfrom extended ring"]
    G --> H{"New successes\n>= W total?"}
    H -->|Yes| D
    H -->|No| F
    D --> I["For each failed replica:\nStoreHint(node, key, siblings)"]
    I --> J["Async: antiEntropy.OnKeyUpdate(key)"]
    J --> K["Return new VectorClock to client"]
```

---

---

## 11. Class Diagram — Core Domain Types

Relationships between the main data structures across `vclock/`, `storage/`, and `common/`.

```mermaid
classDiagram
    class VectorClock {
        -entries map[NodeID]*VectorClockEntry
        +Tick(nodeID NodeID)
        +Get(nodeID NodeID) uint64
        +Merge(other VectorClock)
        +Compare(other VectorClock) Ordering
        +MarshalBinary() []byte
    }

    class VectorClockEntry {
        +NodeID    NodeID
        +Counter   uint64
        +Timestamp int64
    }

    class Sibling {
        +Value     []byte
        +VClock    VectorClock
        +Timestamp int64
        +Tombstone bool
        +ExpiresAt int64
    }

    class SiblingSet {
        +Siblings []Sibling
    }

    class Node {
        +ID               NodeID
        +Addr             string
        +State            NodeState
        +VirtualNodeCount int
        +MissedHeartbeats int
        +LastHeartbeat    time.Time
        -mu               sync.RWMutex
        +RecordHeartbeat()
        +RecordMissedHeartbeat(threshold int)
    }

    class NodeEntry {
        +NodeID    string
        +Addr      string
        +Status    NodeStatus
        +Version   uint64
        +UpdatedAt int64
    }

    class NodeState {
        <<enumeration>>
        Active
        Failed
        Degraded
        Leaving
        Unknown
    }

    VectorClock "1" *-- "*" VectorClockEntry : entries
    Sibling "1" *-- "1" VectorClock : VClock
    SiblingSet "1" *-- "*" Sibling : Siblings
    Node --> NodeState : State
```

---

## 12. Class Diagram — Coordinator & Cluster Wiring

How the main cluster structs hold references to each other (dependency graph as types).

```mermaid
classDiagram
    class Coordinator {
        -nodeID    NodeID
        -ring      *HashRing
        -membership *MembershipManager
        -rpc       RPCClient
        -storage   *Storage
        -readRepair *ReadRepairer
        -antiEntropy *AntiEntropy
        -hintedHandoff *HintedHandoff
        -keyLocks  sync.Map
        -inFlight  int64
        +Get(key) (*SiblingSet, error)
        +Put(key, value, vclock) (VectorClock, error)
        +Delete(key, vclock) error
        +Start()
        +Stop()
    }

    class HashRing {
        -vnodes []VirtualNode
        -nodes  map[NodeID]*Node
        -mu     sync.RWMutex
        +Add(node *Node)
        +Remove(nodeID NodeID)
        +GetPreferenceList(key []byte, n int) []NodeID
    }

    class MembershipManager {
        -peers map[NodeID]*Node
        -mu    sync.RWMutex
        +GetPeer(id NodeID) *Node
        +SetPeerState(id NodeID, s NodeState)
        +ActivePeers() []*Node
    }

    class FailureDetector {
        -peers    *MembershipManager
        -OnNodeFailed    func(NodeID)
        -OnNodeRecovery  func(NodeID)
        +Start()
        +Stop()
    }

    class Gossip {
        -state  map[NodeID]*NodeEntry
        -mu     sync.RWMutex
        -membership *MembershipManager
        +Start()
        +Exchange(payload) GossipState
        +MarkPeer(id NodeID, status NodeStatus)
    }

    class AntiEntropy {
        -merkleTree *MerkleTree
        -storage    *Storage
        -rpc        RPCClient
        +Start()
        +OnKeyUpdate(key []byte, ss *SiblingSet)
        +TriggerSyncWith(nodeID NodeID)
    }

    class HintedHandoff {
        -hints      map[NodeID][]Hint
        -mu         sync.Mutex
        -membership *MembershipManager
        -rpc        RPCClient
        +StoreHint(target NodeID, key []byte, ss *SiblingSet)
        +Start()
    }

    Coordinator --> HashRing
    Coordinator --> MembershipManager
    Coordinator --> AntiEntropy
    Coordinator --> HintedHandoff
    FailureDetector --> MembershipManager
    FailureDetector ..> Coordinator : OnNodeRecovery callback
    FailureDetector ..> Gossip : OnNodeFailed callback
    Gossip --> MembershipManager
    AntiEntropy --> MembershipManager
    HintedHandoff --> MembershipManager
```

---

## 13. Sequence — Gossip Push-Pull Exchange

How membership state propagates between nodes using last-write-wins versioning.

```mermaid
sequenceDiagram
    participant G  as Gossip (Node A)
    participant B  as Node B
    participant C  as Node C
    participant MM as MembershipManager (A)

    note over G: spreadLoop ticks (every 1s)\nPick FanOut=3 random peers

    par Push-pull to each peer
        G  ->>+ B: POST /internal/gossip-exchange\n{nodeA: {status:active, version:5},\n nodeC: {status:failed, version:3}}
        B  -->>- G: {nodeB: {status:active, version:7},\n nodeC: {status:active, version:6}}

        note over G: Merge: nodeC version 6 > 3\n→ update local state: nodeC=active

        G  ->>+ C: POST /internal/gossip-exchange\n{nodeA: {status:active, version:5},\n nodeC: {status:active, version:6}}
        C  -->>- G: {nodeC: {status:active, version:6}, ...}
    end

    G  ->>  MM: SetPeerState(nodeC, Active)

    note over G: LWW rule: higher Version wins\nversion is a monotonic uint64\n(immune to clock skew)
```

---

## 14. Flowchart — Binary Sibling Encoding (Pebble Value Format)

How a `SiblingSet` is serialised to bytes before writing to Pebble.

```mermaid
flowchart TD
    A["SiblingSet{Siblings: [S1, S2, ...]}"] --> B

    subgraph wire["Wire Format (Little-Endian)"]
        B["sibling_count: 2 bytes (uint16)"]
        B --> C

        subgraph sib["For each Sibling"]
            C["vclock_len: 4 bytes (uint32)"]
            C --> D["vclock: N bytes (binary marshalled)"]
            D --> E["flags: 1 byte\n  bit0=Tombstone\n  bit1=Compressed (future)\n  bit2=TTL"]
            E --> F["timestamp: 8 bytes (int64 Unix seconds)"]
            F --> G{TTL flag set?}
            G -->|Yes| H["expires_at: 8 bytes (int64)"]
            G -->|No| I
            H --> I["value_len: 4 bytes (uint32)"]
            I --> J["value: N bytes (raw)"]
        end

        J --> K["crc32: 4 bytes (checksum of all above)"]
    end

    K --> L["Pebble.Set(key, bytes)"]
    L --> M["Magic: 0x47515243 (GQRC)\nVersion: 0x01\nper-record header"]
```

---

## 15. State Diagram — Key Lifecycle (TTL & Tombstones)

The full lifecycle of a key from write through expiry, tombstone, and GC.

```mermaid
stateDiagram-v2
    state "Live" as Live
    state "Tombstone" as Tombstone
    state "GC'd" as GCd

    [*] --> Live : Put(key, value)

    Live --> Live : Put - adds sibling (concurrent VClock)
    Live --> Tombstone : Delete or TTLSweeper expires key

    note right of Live
        Tombstone=false
        ExpiresAt=0 or future Unix timestamp
    end note

    Tombstone --> Live : Put with dominant VClock
    Tombstone --> GCd : tombstoneGCLoop - age > tombstone_ttl

    note right of Tombstone
        Tombstone=true, Value=nil
        Retained for 7 days (default)
        Prevents resurrection via anti-entropy
    end note

    GCd --> [*]

    note right of GCd
        Physically deleted from Pebble
        Risk - stale replica may resurrect key
        if it has not yet synced the tombstone
    end note
```

---

## 16. Sequence — Node Bootstrap (Startup Sequence)

The ordered initialisation of all subsystems in `cmd/quorum/main.go`.

```mermaid
sequenceDiagram
    participant M  as main.go
    participant ST as Storage
    participant MM as MembershipManager
    participant HR as HashRing
    participant FD as FailureDetector
    participant GS as Gossip
    participant AE as AntiEntropy (via Coordinator)
    participant HH as HintedHandoff
    participant TTL as TTLSweeper
    participant SRV as Server (gRPC + HTTP)

    M  ->>  ST:  NewStorage(opts)         ── open Pebble DB
    M  ->>  MM:  NewMembershipManager()   ── seed peers from config
    M  ->>  HR:  NewHashRing()            ── add all N physical nodes
    M  ->>  FD:  NewFailureDetector(MM)
    M  ->>  GS:  NewGossip(MM)
    M  ->>  SRV: NewServer()

    note over M: Wire callbacks\nFD.OnNodeFailed → GS.MarkPeer\nFD.OnNodeRecovery → Coordinator.TriggerAntiEntropyWith

    M  ->>  GS:  Start()    ── spreadLoop goroutine
    M  ->>  FD:  Start()    ── heartbeatLoop goroutine
    M  ->>  HH:  NewHintedHandoff(MM, rpc)
    M  ->>  HH:  Start()    ── replayLoop goroutine
    M  ->>  TTL: NewTTLSweeper(Storage, Coordinator, 1m)
    M  ->>  TTL: Start()    ── sweep goroutine
    M  ->>  SRV: Start()    ── gRPC :7070 + HTTP :8080

    note over M: Bootstrap: wait for quorum\n(>= N/2+1 peers Active)

    loop Until quorum
        FD ->> MM: heartbeat probes all peers
        MM -->> FD: peer states updated
    end

    M  ->>  M:   Wait for OS signal (SIGINT/SIGTERM)
    M  ->>  SRV: Graceful shutdown
    M  ->>  FD:  Stop()
    M  ->>  GS:  Stop()
    M  ->>  HH:  Stop()
    M  ->>  TTL: Stop()
    M  ->>  ST:  Close()
```

---

## 17. Flowchart — Per-Key Write Concurrency

How the Coordinator serialises concurrent writes to the same key while keeping cross-key parallelism.

```mermaid
flowchart TD
    subgraph req1["Goroutine: Put(keyA, v1)"]
        A1["Coordinator.Put(keyA)"] --> B1["keyLocks.LoadOrStore(keyA, &Mutex{})"]
        B1 --> C1["mu.Lock() ← acquires"]
        C1 --> D1["Read local VClock\nTick nodeID counter"]
        D1 --> E1["parallelWrite(keyA, …)"]
        E1 --> F1["mu.Unlock()"]
    end

    subgraph req2["Goroutine: Put(keyA, v2) — concurrent"]
        A2["Coordinator.Put(keyA)"] --> B2["keyLocks.LoadOrStore(keyA, same Mutex)"]
        B2 --> C2["mu.Lock() ← BLOCKS\n(waits for req1 to unlock)"]
        C2 --> D2["Read updated VClock\n(sees req1's tick)"]
        D2 --> E2["parallelWrite(keyA, …)"]
        E2 --> F2["mu.Unlock()"]
    end

    subgraph req3["Goroutine: Put(keyB, v3) — unrelated key"]
        A3["Coordinator.Put(keyB)"] --> B3["keyLocks.LoadOrStore(keyB, &Mutex{})"]
        B3 --> C3["mu.Lock() ← acquires immediately\n(different key, no contention)"]
        C3 --> E3["parallelWrite(keyB, …)"]
        E3 --> F3["mu.Unlock()"]
    end

    F3 --> Z["Key insight:\nsync.Map stores one Mutex per key\nCross-key writes fully parallel\nSame-key writes serialised → monotone VClock"]
```

---

## 18. Flowchart — Quorum Consistency Tradeoff

How N, R, W choices affect consistency guarantees and availability.

```mermaid
flowchart TD
    A["Choose N (replication factor)\ndefault N=3"] --> B["Choose R (read quorum)\nand W (write quorum)"]

    B --> C{"R + W > N ?"}

    C -->|"Yes\ne.g. R=2, W=2, N=3"| D["Strong Consistency\n✓ Read always sees latest write\n✓ R and W overlap guaranteed\n✗ Higher latency (wait for majority)\n✗ Lower write availability"]

    C -->|"No\ne.g. R=1, W=1, N=3"| E["Eventual Consistency\n✓ Lower latency\n✓ Higher availability\n✗ Stale reads possible\n✗ Concurrent writes produce siblings"]

    D --> F["Built-in presets:\nStrongConsistencyConfig() N=3 R=2 W=2\nHighAvailabilityConfig() N=5 R=2 W=2"]
    E --> G["Built-in presets:\nEventualConsistencyConfig() N=3 R=1 W=1"]

    F --> H{"SloppyQuorum\nenabled?"}
    G --> H

    H -->|Yes| I["On node failure:\nUse overflow nodes from ring\nWrite hint for original replica\n→ Higher write availability\n→ Temporary consistency window"]

    H -->|No| J["On node failure:\nReturn QuorumError immediately\n→ Strict but lower availability"]

    I --> K["Read Repair closes window:\nPost-read: async repair stale replicas\nAnti-Entropy: background Merkle sync\nHintedHandoff: replay on recovery"]
```

---

## Diagram Index

| # | Title | Type | Key Insight |
|---|-------|------|-------------|
| 1 | System Context | C4-L1 | External actors: clients, Prometheus, Loki, Grafana |
| 2 | Container | C4-L2 | Two protocols: gRPC :7070 (clients), HTTP :8080 (inter-node) |
| 3 | Component | C4-L3 | 12 cluster components; FailureDetector wires via callbacks |
| 4 | Client Write | Sequence | Parallel fan-out → quorum count → HintedHandoff on failure |
| 5 | Client Read | Sequence | Merge siblings → R quorum → probabilistic read repair |
| 6 | Anti-Entropy | Sequence | Merkle root compare → narrow subtree → key-level merge |
| 7 | Node Recovery | Sequence | FD callback → HintedHandoff replay → AntiEntropy sync |
| 8 | Node Lifecycle | State | Bootstrapping → Active → Degraded → Failed → Active |
| 9 | Key Lookup | Flowchart | xxHash → binary search → clockwise walk → sloppy overflow |
| 10 | Write Quorum | Flowchart | W threshold → sloppy fallback → hint store → VClock return |
| 11 | Core Domain Types | Class | VectorClock → VectorClockEntry; Sibling → SiblingSet |
| 12 | Cluster Wiring | Class | Coordinator owns ring/membership/AE/HH; FD wired via callbacks |
| 13 | Gossip Exchange | Sequence | Push-pull LWW merge; version-based (clock-skew immune) |
| 14 | Binary Encoding | Flowchart | Per-sibling: vclock_len·vclock·flags·timestamp·value + CRC32 |
| 15 | Key Lifecycle | State | Live → Tombstone → GC'd; resurrection risk if tombstone GC too early |
| 16 | Bootstrap Sequence | Sequence | Ordered subsystem init → quorum wait → ready |
| 17 | Per-Key Concurrency | Flowchart | sync.Map per-key mutex: same-key serialised, cross-key parallel |
| 18 | Quorum Tradeoff | Flowchart | R+W>N=strong; R+W≤N=eventual; sloppy quorum bridges failures |
