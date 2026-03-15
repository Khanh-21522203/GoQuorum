package cluster

import (
	"context"
	"testing"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

func testAntiEntropy(t *testing.T, nodeID string, peers []string, rpc RPCClient) *AntiEntropy {
	t.Helper()

	opts := storage.DefaultStorageOptions(t.TempDir(), common.NodeID(nodeID))
	opts.SyncWrites = false
	opts.BlockCacheSize = 8 << 20
	opts.MemTableSize = 8 << 20
	opts.TombstoneGCEnabled = false
	store, err := storage.NewStorage(opts)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ring := NewHashRing(4)
	for _, id := range append([]string{nodeID}, peers...) {
		ring.AddNode(&common.Node{
			ID:               common.NodeID(id),
			Addr:             id + ":7070",
			State:            common.NodeStateActive,
			VirtualNodeCount: 4,
		})
	}

	cfg := config.AntiEntropyConfig{
		Enabled:     true,
		MerkleDepth: 4,
	}

	return NewAntiEntropy(common.NodeID(nodeID), store, ring, rpc, cfg)
}

// divergentMock overrides GetMerkleRoot to return a non-matching hash.
type divergentMock struct {
	mockRPCClient
	divergentRoot []byte
}

func (d *divergentMock) GetMerkleRoot(ctx context.Context, nodeID common.NodeID) ([]byte, error) {
	return d.divergentRoot, nil
}

// ── Same roots: no keys pushed ──────────────────────────────────────────────

func TestAntiEntropyNoPushWhenRootsMatch(t *testing.T) {
	mock := &mockRPCClient{} // GetMerkleRoot returns make([]byte,32) = zeros = same as empty local tree
	ae := testAntiEntropy(t, "n1", []string{"n2"}, mock)

	err := ae.exchangeWithPeer("n2", 0)
	if err != nil {
		t.Fatalf("exchangeWithPeer: %v", err)
	}
	if mock.PutCallCount() != 0 {
		t.Errorf("expected 0 push calls when roots match, got %d", mock.PutCallCount())
	}
}

// ── Different roots: keys in the bucket are pushed ─────────────────────────

func TestAntiEntropyPushesKeysOnDivergence(t *testing.T) {
	divergentRoot := make([]byte, 32)
	divergentRoot[0] = 0xFF
	mock := &divergentMock{divergentRoot: divergentRoot}

	ae := testAntiEntropy(t, "n1", []string{"n2"}, mock)

	// Find a key that maps to bucket 0
	var targetKey []byte
	for i := 0; i < 1000; i++ {
		k := []byte{byte(i)}
		if ae.merkleTree.keyToBucket(k) == 0 {
			targetKey = k
			break
		}
	}
	if targetKey == nil {
		t.Skip("could not find a key mapping to bucket 0")
	}

	vc := vclock.NewVectorClock()
	vc.SetString("n1", 1)
	ae.storage.Put(targetKey, &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte("v"), VClock: vc, Timestamp: 1},
	}})

	err := ae.exchangeWithPeer("n2", 0)
	if err != nil {
		t.Fatalf("exchangeWithPeer: %v", err)
	}

	if mock.PutCallCount() == 0 {
		t.Error("expected push calls when roots diverge")
	}
}

// ── OnKeyUpdate marks tree dirty and changes root ──────────────────────────

func TestAntiEntropyOnKeyUpdate(t *testing.T) {
	mock := &mockRPCClient{}
	ae := testAntiEntropy(t, "n1", []string{"n2"}, mock)

	rootBefore := append([]byte(nil), ae.merkleTree.GetRoot()...)

	vc := vclock.NewVectorClock()
	vc.SetString("n1", 1)
	ss := &storage.SiblingSet{Siblings: []storage.Sibling{{Value: []byte("v"), VClock: vc}}}
	ae.OnKeyUpdate([]byte("k"), ss)

	rootAfter := ae.merkleTree.GetRoot()
	if bytesEqual(rootBefore, rootAfter) {
		t.Error("root should change after OnKeyUpdate")
	}
}

// ── OnKeyDelete restores the root (XOR symmetry) ──────────────────────────

func TestAntiEntropyOnKeyDeleteRestoresRoot(t *testing.T) {
	mock := &mockRPCClient{}
	ae := testAntiEntropy(t, "n1", []string{"n2"}, mock)

	rootEmpty := append([]byte(nil), ae.merkleTree.GetRoot()...)

	vc := vclock.NewVectorClock()
	vc.SetString("n1", 1)
	ss := &storage.SiblingSet{Siblings: []storage.Sibling{{Value: []byte("v"), VClock: vc}}}

	ae.OnKeyUpdate([]byte("k"), ss)
	ae.OnKeyDelete([]byte("k"), ss)

	rootAfterDelete := ae.merkleTree.GetRoot()
	if !bytesEqual(rootEmpty, rootAfterDelete) {
		t.Error("OnKeyDelete should XOR-cancel OnKeyUpdate, restoring original root")
	}
}
