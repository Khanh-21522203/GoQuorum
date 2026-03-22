package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

// integrationNode holds all components for a single node in the test cluster.
type integrationNode struct {
	id          common.NodeID
	storage     *storage.Storage
	coordinator *Coordinator
	membership  *MembershipManager
	httpServer  *httptest.Server
}

// newIntegrationCluster creates a fully wired N-node cluster backed by real
// HTTP servers and Pebble storage.
func newIntegrationCluster(
	t testing.TB,
	nodeIDs []string,
	quorumCfg config.QuorumConfig,
	repairCfg config.ReadRepairConfig,
) []*integrationNode {
	t.Helper()

	n := len(nodeIDs)
	nodes := make([]*integrationNode, n)

	// ---- Phase 1: storage ----
	for i, id := range nodeIDs {
		opts := storage.DefaultStorageOptions(t.TempDir(), common.NodeID(id))
		opts.SyncWrites = false
		opts.TombstoneGCEnabled = false
		opts.BlockCacheSize = 8 << 20
		opts.MemTableSize = 8 << 20
		s, err := storage.NewStorage(opts)
		if err != nil {
			t.Fatalf("NewStorage(%s): %v", id, err)
		}
		t.Cleanup(func() { s.Close() })
		nodes[i] = &integrationNode{id: common.NodeID(id), storage: s}
	}

	// ---- Phase 2: shared hash ring ----
	ring := NewHashRing(4)
	for _, id := range nodeIDs {
		if err := ring.AddNode(&common.Node{
			ID:               common.NodeID(id),
			Addr:             id + ":7070",
			State:            common.NodeStateActive,
			VirtualNodeCount: 4,
		}); err != nil {
			t.Fatalf("ring.AddNode(%s): %v", id, err)
		}
	}

	// ---- Phase 3: HTTP servers ----
	// We need the storage references to build the handlers, but we don't have
	// membership yet. Build placeholder servers that capture the node index via
	// closure so they can look up the correct storage at request time.
	for idx := range nodes {
		node := nodes[idx] // capture

		mux := http.NewServeMux()

		// POST /internal/replicate
		mux.HandleFunc("/internal/replicate", func(w http.ResponseWriter, r *http.Request) {
			var req replicateReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			sib := storage.Sibling{
				Value:     req.Sibling.Value,
				VClock:    dtoToVclock(req.Sibling.Context),
				Tombstone: req.Sibling.Tombstone,
				Timestamp: req.Sibling.Timestamp,
			}
			ss := &storage.SiblingSet{Siblings: []storage.Sibling{sib}}
			if err := node.storage.Put(req.Key, ss); err != nil {
				json.NewEncoder(w).Encode(replicateResp{Success: false, Error: err.Error()}) //nolint:errcheck
				return
			}
			json.NewEncoder(w).Encode(replicateResp{Success: true}) //nolint:errcheck
		})

		// POST /internal/read
		mux.HandleFunc("/internal/read", func(w http.ResponseWriter, r *http.Request) {
			var req internalReadReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ss, err := node.storage.GetRaw(req.Key)
			if err == common.ErrKeyNotFound {
				json.NewEncoder(w).Encode(internalReadResp{Found: false}) //nolint:errcheck
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(internalReadResp{ //nolint:errcheck
				Found:    true,
				Siblings: siblingSetToDTO(ss),
			})
		})

		// POST /internal/heartbeat
		mux.HandleFunc("/internal/heartbeat", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(heartbeatResp{ResponderID: string(node.id)}) //nolint:errcheck
		})

		// POST /internal/merkle-root
		mux.HandleFunc("/internal/merkle-root", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(merkleRootResp{MerkleRoot: []byte{}}) //nolint:errcheck
		})

		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		node.httpServer = srv
	}

	// ---- Phase 4: membership managers ----
	members := make([]config.MemberConfig, n)
	for i, id := range nodeIDs {
		members[i] = config.MemberConfig{
			ID:       common.NodeID(id),
			Addr:     id + ":7070",
			HTTPAddr: id + ":8080", // placeholder; will be patched below
		}
	}

	for idx, id := range nodeIDs {
		clusterCfg := config.ClusterConfig{
			NodeID:            common.NodeID(id),
			ListenAddr:        id + ":7070",
			Members:           members,
			FailureThreshold:  3,
			HeartbeatInterval: time.Second,
			HeartbeatTimeout:  2 * time.Second,
			BootstrapTimeout:  60 * time.Second,
		}
		mm := NewMembershipManager(clusterCfg, "test")
		mm.SetLocalStatus(NodeStatusActive)
		nodes[idx].membership = mm
	}

	// ---- Phase 5: patch peer addresses with real httptest URLs ----
	for _, node := range nodes {
		mm := node.membership
		mm.mu.Lock()
		for _, peer := range mm.peers {
			// Find the integrationNode that owns this peer ID.
			for _, other := range nodes {
				if other.id == peer.NodeID {
					peer.HTTPAddr = strings.TrimPrefix(other.httpServer.URL, "http://")
					peer.Status = NodeStatusActive
					break
				}
			}
		}
		mm.mu.Unlock()
	}

	// ---- Phase 6: RPC clients and coordinators ----
	aeCfg := config.AntiEntropyConfig{Enabled: false, MerkleDepth: 4}
	for _, node := range nodes {
		rpc := NewGRPCClient(config.DefaultConnectionConfig(), node.membership)
		node.coordinator = NewCoordinator(
			node.id, ring, node.storage, rpc, node.membership,
			quorumCfg, repairCfg, aeCfg,
		)
	}

	return nodes
}

// nodeByID returns the integrationNode with the given ID.
func nodeByIDFn(nodes []*integrationNode, id common.NodeID) *integrationNode {
	for _, n := range nodes {
		if n.id == id {
			return n
		}
	}
	return nil
}

// ── Test 1: Put replicates to at least W nodes ─────────────────────────────

func TestIntegration_PutReplicatesToAllNodes(t *testing.T) {
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 2}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}
	nodes := newIntegrationCluster(t, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)

	ctx := context.Background()
	_, err := nodes[0].coordinator.Put(ctx, "key1", []byte("val1"), vclock.NewVectorClock())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// At least W=2 nodes must have the key.
	found := 0
	for _, node := range nodes {
		if _, err := node.storage.GetRaw([]byte("key1")); err == nil {
			found++
		}
	}
	if found < quorumCfg.W {
		t.Errorf("expected key on at least %d nodes, got %d", quorumCfg.W, found)
	}
}

// ── Test 2: Read aggregates from replicas ─────────────────────────────────

func TestIntegration_ReadAggregatesFromReplicas(t *testing.T) {
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 2}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}
	nodes := newIntegrationCluster(t, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)

	// Write directly to n1 and n2 storage (bypass coordinator).
	vc := vclock.NewVectorClock()
	vc.SetString("n1", 1)
	sib := storage.Sibling{Value: []byte("direct"), VClock: vc, Timestamp: time.Now().Unix()}
	ss := &storage.SiblingSet{Siblings: []storage.Sibling{sib}}
	if err := nodes[0].storage.Put([]byte("directkey"), ss); err != nil {
		t.Fatalf("direct put n1: %v", err)
	}
	if err := nodes[1].storage.Put([]byte("directkey"), ss); err != nil {
		t.Fatalf("direct put n2: %v", err)
	}

	// Read via nodes[0] coordinator — should succeed with R=2.
	siblings, err := nodes[0].coordinator.Get(context.Background(), "directkey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(siblings) == 0 || string(siblings[0].Value) != "direct" {
		t.Errorf("unexpected siblings: %+v", siblings)
	}
}

// ── Test 3: Quorum write fails when nodes unreachable ─────────────────────

func TestIntegration_QuorumWriteFailsWhenNodesUnreachable(t *testing.T) {
	// Fail server: always returns 500.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "node down", http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)
	failAddr := strings.TrimPrefix(failSrv.URL, "http://")

	// Build a single-node cluster for n1, with n2/n3 pointing to the fail server.
	nodeIDs := []string{"n1", "n2", "n3"}
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 3} // W=3 requires all
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}

	// Build storage and ring.
	opts := storage.DefaultStorageOptions(t.TempDir(), "n1")
	opts.SyncWrites = false
	opts.TombstoneGCEnabled = false
	opts.BlockCacheSize = 8 << 20
	opts.MemTableSize = 8 << 20
	n1storage, err := storage.NewStorage(opts)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { n1storage.Close() })

	ring := NewHashRing(4)
	for _, id := range nodeIDs {
		ring.AddNode(&common.Node{ //nolint:errcheck
			ID: common.NodeID(id), Addr: id + ":7070",
			State: common.NodeStateActive, VirtualNodeCount: 4,
		})
	}

	members := []config.MemberConfig{
		{ID: "n1", Addr: "n1:7070", HTTPAddr: "n1:8080"},
		{ID: "n2", Addr: "n2:7070", HTTPAddr: "n2:8080"},
		{ID: "n3", Addr: "n3:7070", HTTPAddr: "n3:8080"},
	}
	mm := NewMembershipManager(config.ClusterConfig{
		NodeID:            "n1",
		ListenAddr:        "n1:7070",
		Members:           members,
		FailureThreshold:  3,
		HeartbeatInterval: time.Second,
		HeartbeatTimeout:  2 * time.Second,
		BootstrapTimeout:  60 * time.Second,
	}, "test")
	mm.SetLocalStatus(NodeStatusActive)

	// Patch n2 and n3 to point to the fail server.
	mm.mu.Lock()
	mm.peers["n2"].HTTPAddr = failAddr
	mm.peers["n2"].Status = NodeStatusActive
	mm.peers["n3"].HTTPAddr = failAddr
	mm.peers["n3"].Status = NodeStatusActive
	mm.mu.Unlock()

	rpc := NewGRPCClient(config.DefaultConnectionConfig(), mm)
	coordinator := NewCoordinator("n1", ring, n1storage, rpc, mm,
		quorumCfg, repairCfg, config.AntiEntropyConfig{Enabled: false, MerkleDepth: 4})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = coordinator.Put(ctx, "failkey", []byte("val"), vclock.NewVectorClock())
	if err == nil {
		t.Fatal("expected QuorumError, got nil")
	}
	var qErr *common.QuorumError
	if !errors.As(err, &qErr) {
		t.Errorf("expected *common.QuorumError, got %T: %v", err, err)
	}
}

// ── Test 4: Read repair fixes stale replica ────────────────────────────────

func TestIntegration_ReadRepairFixesStalReplica(t *testing.T) {
	// R=3 ensures all 3 responses are collected before repair fires.
	quorumCfg := config.QuorumConfig{N: 3, R: 3, W: 2}
	repairCfg := config.ReadRepairConfig{
		Enabled:     true,
		Async:       false, // synchronous so we can assert after Get
		Timeout:     2 * time.Second,
		Probability: 1.0,
	}
	nodeIDs := []string{"n1", "n2", "n3"}
	nodes := newIntegrationCluster(t, nodeIDs, quorumCfg, repairCfg)

	// Build a nodeID→node lookup.
	byID := make(map[common.NodeID]*integrationNode, len(nodes))
	for _, n := range nodes {
		byID[n.id] = n
	}

	// Find preference list for "repairkey".
	ring := nodes[0].coordinator.ring
	prefList, err := ring.GetPreferenceList("repairkey", 3)
	if err != nil {
		t.Fatalf("GetPreferenceList: %v", err)
	}

	// Write directly to prefList[0] and prefList[1] only.
	vc := vclock.NewVectorClock()
	vc.SetString(string(prefList[0]), 1)
	sib := storage.Sibling{
		Value:     []byte("repaired"),
		VClock:    vc,
		Timestamp: time.Now().Unix(),
	}
	ss := &storage.SiblingSet{Siblings: []storage.Sibling{sib}}

	for _, id := range prefList[:2] {
		if err := byID[id].storage.Put([]byte("repairkey"), ss); err != nil {
			t.Fatalf("direct put %s: %v", id, err)
		}
	}

	// Verify prefList[2] does NOT have the key.
	staleNode := byID[prefList[2]]
	if _, err := staleNode.storage.GetRaw([]byte("repairkey")); err == nil {
		t.Fatal("expected stale node to not have key before repair")
	}

	// Read via prefList[0]'s coordinator — repair should be triggered for prefList[2].
	coordinator := byID[prefList[0]].coordinator
	siblings, err := coordinator.Get(context.Background(), "repairkey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(siblings) == 0 {
		t.Fatal("expected at least one sibling from Get")
	}

	// After synchronous read repair, prefList[2] should now have the key.
	if _, err := staleNode.storage.GetRaw([]byte("repairkey")); err != nil {
		t.Errorf("expected stale node to have key after repair, got: %v", err)
	}
}

// ── Test 5: Delete propagates to replicas ────────────────────────────────

func TestIntegration_DeletePropagatesToReplicas(t *testing.T) {
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 2}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}
	nodes := newIntegrationCluster(t, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)

	ctx := context.Background()

	// Put "delkey".
	vc, err := nodes[0].coordinator.Put(ctx, "delkey", []byte("toDelete"), vclock.NewVectorClock())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Delete "delkey".
	if err := nodes[0].coordinator.Delete(ctx, "delkey", vc); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Get should return ErrKeyNotFound.
	_, err = nodes[0].coordinator.Get(ctx, "delkey")
	if !errors.Is(err, common.ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound after delete, got: %v", err)
	}

	// At least W nodes should have a tombstone.
	tombstoneCount := 0
	for _, node := range nodes {
		ss, err := node.storage.GetRaw([]byte("delkey"))
		if err != nil {
			continue
		}
		for _, sib := range ss.Siblings {
			if sib.Tombstone {
				tombstoneCount++
				break
			}
		}
	}
	if tombstoneCount < quorumCfg.W {
		t.Errorf("expected tombstone on at least %d nodes, got %d", quorumCfg.W, tombstoneCount)
	}
}

// ── Test 6: Concurrent writes create siblings ─────────────────────────────

func TestIntegration_ConcurrentWritesCreateSiblings(t *testing.T) {
	quorumCfg := config.QuorumConfig{N: 3, R: 3, W: 3}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}
	nodes := newIntegrationCluster(t, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)

	ctx := context.Background()

	// Two independent puts with empty (concurrent) vector clocks.
	_, err1 := nodes[0].coordinator.Put(ctx, "sibkey", []byte("v1"), vclock.NewVectorClock())
	_, err2 := nodes[1].coordinator.Put(ctx, "sibkey", []byte("v2"), vclock.NewVectorClock())

	if err1 != nil {
		t.Fatalf("Put v1: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("Put v2: %v", err2)
	}

	// At least one node should have multiple siblings (concurrent versions).
	found := false
	for _, node := range nodes {
		ss, err := node.storage.GetRaw([]byte("sibkey"))
		if err != nil {
			continue
		}
		if len(ss.Siblings) >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one node to have >= 2 siblings after concurrent writes")
	}
}

// ── Test 7: Vector clock causality preserved ──────────────────────────────

func TestIntegration_VectorClockCausalityPreserved(t *testing.T) {
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 2}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}
	nodes := newIntegrationCluster(t, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)

	ctx := context.Background()

	// First write.
	vc1, err := nodes[0].coordinator.Put(ctx, "causkey", []byte("v1"), vclock.NewVectorClock())
	if err != nil {
		t.Fatalf("Put v1: %v", err)
	}

	// Causally after: use vc1 as base context.
	_, err = nodes[0].coordinator.Put(ctx, "causkey", []byte("v2"), vc1)
	if err != nil {
		t.Fatalf("Put v2: %v", err)
	}

	// Read should return exactly 1 sibling (v2 dominates v1).
	siblings, err := nodes[0].coordinator.Get(ctx, "causkey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(siblings) != 1 {
		t.Errorf("expected 1 sibling (v2 dominates v1), got %d: %+v", len(siblings), siblings)
	}
	if string(siblings[0].Value) != "v2" {
		t.Errorf("expected value 'v2', got %q", siblings[0].Value)
	}
}

// ── Test 8: TTL key expiry ─────────────────────────────────────────────────

func TestIntegration_TTLKeyExpiry(t *testing.T) {
	// Use N=1, R=1, W=1 so the coordinator only reads from its own local
	// storage, which correctly enforces TTL expiry via GetRaw filtering.
	// (The HTTP read path does not transmit ExpiresAt in the DTO, so a
	// remote-only read would see a non-expired sibling.)
	quorumCfg := config.QuorumConfig{N: 1, R: 1, W: 1}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}
	nodes := newIntegrationCluster(t, []string{"n1"}, quorumCfg, repairCfg)

	ctx := context.Background()

	// Put with TTL of 1 second.
	_, err := nodes[0].coordinator.Put(ctx, "ttlkey", []byte("expires"), vclock.NewVectorClock(),
		PutOptions{TTLSeconds: 1})
	if err != nil {
		t.Fatalf("Put with TTL: %v", err)
	}

	// Immediate read should succeed.
	siblings, err := nodes[0].coordinator.Get(ctx, "ttlkey")
	if err != nil {
		t.Fatalf("immediate Get: %v", err)
	}
	if len(siblings) == 0 {
		t.Fatal("expected value immediately after TTL put")
	}

	// Wait for TTL to expire.
	time.Sleep(2 * time.Second)

	// Read after expiry should return ErrKeyNotFound (local GetRaw filters expired siblings).
	_, err = nodes[0].coordinator.Get(ctx, "ttlkey")
	if !errors.Is(err, common.ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound after TTL expiry, got: %v", err)
	}
}

// ── Test 9: Hinted handoff delivers writes ────────────────────────────────

func TestIntegration_HintedHandoffDelivers(t *testing.T) {
	// W=1 so write succeeds with just the local node.
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 1}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}

	// We'll build the cluster manually to get fine-grained control over n3.
	nodeIDs := []string{"n1", "n2", "n3"}

	// Fail server for n2 and n3 during initial write.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "node down", http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)
	failAddr := strings.TrimPrefix(failSrv.URL, "http://")

	// Build storage for all nodes.
	makeStorage := func(id string) *storage.Storage {
		opts := storage.DefaultStorageOptions(t.TempDir(), common.NodeID(id))
		opts.SyncWrites = false
		opts.TombstoneGCEnabled = false
		opts.BlockCacheSize = 8 << 20
		opts.MemTableSize = 8 << 20
		s, err := storage.NewStorage(opts)
		if err != nil {
			t.Fatalf("NewStorage(%s): %v", id, err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}

	n1Storage := makeStorage("n1")
	n2Storage := makeStorage("n2")
	n3Storage := makeStorage("n3")
	_ = n2Storage // used via HTTP server below

	// Build shared ring.
	ring := NewHashRing(4)
	for _, id := range nodeIDs {
		ring.AddNode(&common.Node{ //nolint:errcheck
			ID: common.NodeID(id), Addr: id + ":7070",
			State: common.NodeStateActive, VirtualNodeCount: 4,
		})
	}

	members := []config.MemberConfig{
		{ID: "n1", Addr: "n1:7070", HTTPAddr: "n1:8080"},
		{ID: "n2", Addr: "n2:7070", HTTPAddr: "n2:8080"},
		{ID: "n3", Addr: "n3:7070", HTTPAddr: "n3:8080"},
	}

	mm := NewMembershipManager(config.ClusterConfig{
		NodeID:            "n1",
		ListenAddr:        "n1:7070",
		Members:           members,
		FailureThreshold:  3,
		HeartbeatInterval: time.Second,
		HeartbeatTimeout:  2 * time.Second,
		BootstrapTimeout:  60 * time.Second,
	}, "test")
	mm.SetLocalStatus(NodeStatusActive)

	// Point n2 and n3 at the fail server initially.
	mm.mu.Lock()
	mm.peers["n2"].HTTPAddr = failAddr
	mm.peers["n2"].Status = NodeStatusActive
	mm.peers["n3"].HTTPAddr = failAddr
	mm.peers["n3"].Status = NodeStatusActive
	mm.mu.Unlock()

	rpc := NewGRPCClient(config.DefaultConnectionConfig(), mm)
	hh := NewHintedHandoff(mm, rpc, "n1")

	aeCfg := config.AntiEntropyConfig{Enabled: false, MerkleDepth: 4}
	coordinator := NewCoordinator("n1", ring, n1Storage, rpc, mm, quorumCfg, repairCfg, aeCfg)
	coordinator.SetHintedHandoff(hh)

	// Write "hintkey" — succeeds with W=1 (local write).
	ctx := context.Background()
	_, err := coordinator.Put(ctx, "hintkey", []byte("hinted"), vclock.NewVectorClock())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// n3 should have at least one pending hint.
	if hh.HintCount("n3") == 0 {
		t.Error("expected HintCount(n3) > 0 after failed write")
	}

	// Now bring up a real n3 HTTP server.
	n3Mux := http.NewServeMux()
	n3Mux.HandleFunc("/internal/replicate", func(w http.ResponseWriter, r *http.Request) {
		var req replicateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sib := storage.Sibling{
			Value:     req.Sibling.Value,
			VClock:    dtoToVclock(req.Sibling.Context),
			Tombstone: req.Sibling.Tombstone,
			Timestamp: req.Sibling.Timestamp,
		}
		ss := &storage.SiblingSet{Siblings: []storage.Sibling{sib}}
		if err := n3Storage.Put(req.Key, ss); err != nil {
			json.NewEncoder(w).Encode(replicateResp{Success: false, Error: err.Error()}) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(replicateResp{Success: true}) //nolint:errcheck
	})
	n3Srv := httptest.NewServer(n3Mux)
	t.Cleanup(n3Srv.Close)
	n3Addr := strings.TrimPrefix(n3Srv.URL, "http://")

	// Update n1's view of n3 to the real server.
	mm.mu.Lock()
	mm.peers["n3"].HTTPAddr = n3Addr
	mm.peers["n3"].Status = NodeStatusActive
	mm.mu.Unlock()

	// Replay hints for n3.
	delivered := hh.replayHintsForNode("n3")
	if delivered == 0 {
		t.Error("expected at least one hint delivered to n3")
	}

	// n3 storage should now have "hintkey".
	if _, err := n3Storage.GetRaw([]byte("hintkey")); err != nil {
		t.Errorf("expected n3 to have hintkey after handoff replay, got: %v", err)
	}
}

// ── Test 10: BatchPut and BatchGet ────────────────────────────────────────

func TestIntegration_BatchPutAndGet(t *testing.T) {
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 2}
	repairCfg := config.ReadRepairConfig{Enabled: false, Timeout: time.Second, Probability: 1.0}
	nodes := newIntegrationCluster(t, []string{"n1", "n2", "n3"}, quorumCfg, repairCfg)

	ctx := context.Background()
	items := []BatchPutItem{
		{Key: "batch1", Value: []byte("alpha"), Context: vclock.NewVectorClock()},
		{Key: "batch2", Value: []byte("beta"), Context: vclock.NewVectorClock()},
		{Key: "batch3", Value: []byte("gamma"), Context: vclock.NewVectorClock()},
	}

	putResults := nodes[0].coordinator.BatchPut(ctx, items)
	for _, r := range putResults {
		if r.Error != nil {
			t.Errorf("BatchPut %s: %v", r.Key, r.Error)
		}
	}

	keys := []string{"batch1", "batch2", "batch3"}
	expected := map[string]string{
		"batch1": "alpha",
		"batch2": "beta",
		"batch3": "gamma",
	}

	getResults := nodes[0].coordinator.BatchGet(ctx, keys)
	for _, r := range getResults {
		if r.Error != nil {
			t.Errorf("BatchGet %s: %v", r.Key, r.Error)
			continue
		}
		if len(r.Siblings) == 0 {
			t.Errorf("BatchGet %s: no siblings", r.Key)
			continue
		}
		if string(r.Siblings[0].Value) != expected[r.Key] {
			t.Errorf("BatchGet %s: expected %q, got %q", r.Key, expected[r.Key], r.Siblings[0].Value)
		}
	}
}
