package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

func testStorageForCoordinator(t *testing.T, nodeID string) *storage.Storage {
	t.Helper()
	opts := storage.DefaultStorageOptions(t.TempDir(), common.NodeID(nodeID))
	opts.SyncWrites = false
	opts.BlockCacheSize = 8 << 20
	opts.MemTableSize = 8 << 20
	opts.TombstoneGCEnabled = false
	s, err := storage.NewStorage(opts)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testCoordinator(t *testing.T, nodeID string, peers []string, rpc RPCClient) *Coordinator {
	t.Helper()

	ring := NewHashRing(4)
	allNodes := append([]string{nodeID}, peers...)
	for _, id := range allNodes {
		node := &common.Node{ID: common.NodeID(id), Addr: id + ":7070", State: common.NodeStateActive, VirtualNodeCount: 4}
		ring.AddNode(node)
	}

	store := testStorageForCoordinator(t, nodeID)

	members := []config.MemberConfig{{ID: common.NodeID(nodeID), Addr: nodeID + ":7070", HTTPAddr: nodeID + ":8080"}}
	for _, p := range peers {
		members = append(members, config.MemberConfig{ID: common.NodeID(p), Addr: p + ":7070", HTTPAddr: p + ":8080"})
	}
	membership := NewMembershipManager(config.ClusterConfig{NodeID: common.NodeID(nodeID), Members: members, FailureThreshold: 3}, "test")

	quorumCfg := config.QuorumConfig{N: len(allNodes), R: 1, W: len(allNodes) - 1}
	if quorumCfg.W < 1 {
		quorumCfg.W = 1
	}
	repairCfg := config.ReadRepairConfig{Enabled: false}
	aeCfg := config.AntiEntropyConfig{Enabled: false, MerkleDepth: 4}

	c := NewCoordinator(common.NodeID(nodeID), ring, store, rpc, membership, quorumCfg, repairCfg, aeCfg)
	return c
}

// ── Put succeeds when W quorum replicas respond ────────────────────────────

func TestCoordinatorPutQuorumSuccess(t *testing.T) {
	mock := &mockRPCClient{}
	c := testCoordinator(t, "n1", []string{"n2", "n3"}, mock)

	ctx := context.Background()
	newVC, err := c.Put(ctx, "mykey", []byte("myval"), vclock.NewVectorClock())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if newVC.IsEmpty() {
		t.Error("expected non-empty VectorClock from Put")
	}
}

// ── Put fails when all remote RPCs fail (quorum not met) ──────────────────

func TestCoordinatorPutQuorumFailure(t *testing.T) {
	rpcErr := errors.New("connection refused")
	mock := &mockRPCClient{
		putFn: func(_ context.Context, _ common.NodeID, _ []byte, _ *storage.SiblingSet) error {
			return rpcErr
		},
	}
	// Single-node coordinator (N=1, W=1): even if remote fails, local write succeeds
	// For quorum failure, use N=3, W=3 with 2 remote nodes both failing
	ring := NewHashRing(4)
	for _, id := range []string{"n1", "n2", "n3"} {
		ring.AddNode(&common.Node{ID: common.NodeID(id), Addr: id + ":7070", State: common.NodeStateActive, VirtualNodeCount: 4})
	}
	store := testStorageForCoordinator(t, "n1")
	members := []config.MemberConfig{
		{ID: "n1", Addr: "n1:7070", HTTPAddr: "n1:8080"},
		{ID: "n2", Addr: "n2:7070", HTTPAddr: "n2:8080"},
		{ID: "n3", Addr: "n3:7070", HTTPAddr: "n3:8080"},
	}
	membership := NewMembershipManager(config.ClusterConfig{NodeID: "n1", Members: members, FailureThreshold: 3}, "test")
	quorumCfg := config.QuorumConfig{N: 3, R: 2, W: 3} // W=3 requires all nodes
	c := NewCoordinator("n1", ring, store, mock, membership, quorumCfg,
		config.ReadRepairConfig{Enabled: false}, config.AntiEntropyConfig{Enabled: false, MerkleDepth: 4})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := c.Put(ctx, "key", []byte("val"), vclock.NewVectorClock())
	if err == nil {
		t.Fatal("expected quorum error, got nil")
	}
	var qErr *common.QuorumError
	if !errors.As(err, &qErr) {
		t.Errorf("expected QuorumError, got %T: %v", err, err)
	}
}

// ── Get returns ErrKeyNotFound for unknown key ────────────────────────────

func TestCoordinatorGetMissingKey(t *testing.T) {
	mock := &mockRPCClient{}
	c := testCoordinator(t, "n1", nil, mock)

	_, err := c.Get(context.Background(), "nosuchkey")
	if err != common.ErrKeyNotFound {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

// ── Put then Get returns the written value ────────────────────────────────

func TestCoordinatorPutThenGet(t *testing.T) {
	mock := &mockRPCClient{}
	c := testCoordinator(t, "n1", nil, mock)

	ctx := context.Background()
	_, err := c.Put(ctx, "k", []byte("hello"), vclock.NewVectorClock())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	siblings, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(siblings) != 1 || string(siblings[0].Value) != "hello" {
		t.Errorf("unexpected siblings: %+v", siblings)
	}
}

// ── Delete marks key as gone ──────────────────────────────────────────────

func TestCoordinatorDeleteMakesKeyGone(t *testing.T) {
	mock := &mockRPCClient{}
	c := testCoordinator(t, "n1", nil, mock)

	ctx := context.Background()
	vc, _ := c.Put(ctx, "k", []byte("v"), vclock.NewVectorClock())

	if err := c.Delete(ctx, "k", vc); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := c.Get(ctx, "k")
	if err != common.ErrKeyNotFound {
		t.Errorf("expected ErrKeyNotFound after Delete, got %v", err)
	}
}

// ── InFlightCount tracks concurrent requests ──────────────────────────────

func TestCoordinatorInFlightCount(t *testing.T) {
	mock := &mockRPCClient{}
	c := testCoordinator(t, "n1", nil, mock)

	if c.InFlightCount() != 0 {
		t.Error("expected 0 in-flight before any request")
	}
}
