package antientropy

import (
	"bytes"
	"sort"
	"sync"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
)

type memStorage struct {
	mu   sync.Mutex
	data map[string]*adapter.SiblingSet
}

func newMemStorage() *memStorage {
	return &memStorage{data: make(map[string]*adapter.SiblingSet)}
}

func (fs *memStorage) put(key []byte, ss *adapter.SiblingSet) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.data[string(key)] = ss
}

func (fs *memStorage) Get(key []byte, done func(*adapter.SiblingSet, error)) {
	fs.mu.Lock()
	ss := fs.data[string(key)]
	fs.mu.Unlock()
	done(ss, nil)
}
func (fs *memStorage) GetRaw(key []byte, done func(*adapter.SiblingSet, error)) {
	fs.Get(key, done)
}
func (fs *memStorage) Put(key []byte, siblings *adapter.SiblingSet, done func(error)) {
	fs.put(key, siblings)
	done(nil)
}
func (fs *memStorage) Delete(key []byte, _ vclock.VectorClock, done func(error)) {
	fs.mu.Lock()
	delete(fs.data, string(key))
	fs.mu.Unlock()
	done(nil)
}
func (fs *memStorage) LocalNodeID() node.NodeID { return "local" }
func (fs *memStorage) Stats() adapter.Stats     { return adapter.Stats{} }
func (fs *memStorage) Close() error             { return nil }

func (fs *memStorage) Scan(startKey, endKey []byte, onEntry adapter.ScanFunc, done func(error)) {
	fs.mu.Lock()
	keys := make([]string, 0, len(fs.data))
	for k := range fs.data {
		if (len(startKey) == 0 || k >= string(startKey)) &&
			(len(endKey) == 0 || k <= string(endKey)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	entries := make([]struct {
		k string
		v *adapter.SiblingSet
	}, len(keys))
	for i, k := range keys {
		entries[i] = struct {
			k string
			v *adapter.SiblingSet
		}{k, fs.data[k]}
	}
	fs.mu.Unlock()

	for _, e := range entries {
		if !onEntry([]byte(e.k), e.v) {
			break
		}
	}
	done(nil)
}

type fakeTransport struct {
	mu     sync.Mutex
	roots  map[node.NodeID][]byte
	putErr error
	puts   int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{roots: make(map[node.NodeID][]byte)}
}

func (ft *fakeTransport) RemotePut(id node.NodeID, corrID uint64, key []byte, _ *adapter.SiblingSet) error {
	ft.mu.Lock()
	ft.puts++
	err := ft.putErr
	ft.mu.Unlock()
	return err
}

func (ft *fakeTransport) RemoteGet(node.NodeID, uint64, []byte) error { return nil }
func (ft *fakeTransport) Heartbeat(node.NodeID, uint64) error         { return nil }
func (ft *fakeTransport) GetMerkleRoot(id node.NodeID, corrID uint64) error {
	ft.mu.Lock()
	root := ft.roots[id]
	ft.mu.Unlock()
	_ = root
	return nil
}
func (ft *fakeTransport) NotifyLeaving(node.NodeID, uint64) error { return nil }
func (ft *fakeTransport) GossipExchange(node.NodeID, uint64, []adapter.GossipEntry) error {
	return nil
}
func (ft *fakeTransport) Dial(node.NodeID, string) error { return nil }
func (ft *fakeTransport) Close() error                   { return nil }

func TestAntiEntropy_Build(t *testing.T) {
	ring := hashring.NewHashRing(256)
	_ = ring.AddNode(&node.Node{ID: "n1", State: node.NodeStateActive, VirtualNodeCount: 256})

	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	store := newMemStorage()
	tr := newFakeTransport()
	ae := NewAntiEntropy("n1", store, ring, tr, cfg)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	store.put([]byte("k2"), sibling("n1", 1, []byte("v2")))

	if err := ae.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got := ae.GetMerkleRoot()
	want := NewMerkleTree(cfg.MerkleDepth)
	want.UpdateKey([]byte("k1"), sibling("n1", 1, []byte("v1")))
	want.UpdateKey([]byte("k2"), sibling("n1", 1, []byte("v2")))
	if !bytes.Equal(got, want.GetRoot()) {
		t.Errorf("root = %x, want %x", got, want.GetRoot())
	}
}

func TestAntiEntropy_TriggerWithPeer_MatchingRoot(t *testing.T) {
	ring := hashring.NewHashRing(256)
	_ = ring.AddNode(&node.Node{ID: "n1", State: node.NodeStateActive, VirtualNodeCount: 256})

	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	store := newMemStorage()
	tr := newFakeTransport()
	ae := NewAntiEntropy("n1", store, ring, tr, cfg)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	_ = ae.Build()

	ae.OnMerkleRootResult("n2", ae.GetMerkleRoot(), nil)

	if tr.puts != 0 {
		t.Errorf("expected 0 puts when roots match, got %d", tr.puts)
	}
}

func TestAntiEntropy_TriggerWithPeer_DivergingRoot(t *testing.T) {
	ring := hashring.NewHashRing(256)
	_ = ring.AddNode(&node.Node{ID: "n1", State: node.NodeStateActive, VirtualNodeCount: 256})

	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	store := newMemStorage()
	tr := newFakeTransport()
	ae := NewAntiEntropy("n1", store, ring, tr, cfg)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	_ = ae.Build()

	divergingRoot := bytes.Repeat([]byte{0xFF}, hashSize)
	ae.OnMerkleRootResult("n2", divergingRoot, nil)

	if tr.puts == 0 {
		t.Error("expected RemotePut call when roots diverge, got 0")
	}
}

func TestAntiEntropy_SyncWithPeers(t *testing.T) {
	ring := hashring.NewHashRing(256)
	_ = ring.AddNode(&node.Node{ID: "n1", State: node.NodeStateActive, VirtualNodeCount: 256})

	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	store := newMemStorage()
	tr := newFakeTransport()
	ae := NewAntiEntropy("n1", store, ring, tr, cfg)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	store.put([]byte("k2"), sibling("n1", 1, []byte("v2")))

	var doneErr error
	ae.SyncWithPeers([]node.NodeID{"n2", "n3"}, func(err error) {
		doneErr = err
	})

	if doneErr != nil {
		t.Fatalf("SyncWithPeers failed: %v", doneErr)
	}
	if tr.puts != 4 {
		t.Errorf("expected 4 RemotePut calls, got %d", tr.puts)
	}
}

func TestAntiEntropy_OnKeyUpdateAndDelete(t *testing.T) {
	ring := hashring.NewHashRing(256)
	_ = ring.AddNode(&node.Node{ID: "n1", State: node.NodeStateActive, VirtualNodeCount: 256})

	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	ae := NewAntiEntropy("n1", newMemStorage(), ring, newFakeTransport(), cfg)

	rootEmpty := ae.GetMerkleRoot()

	ss := sibling("n1", 1, []byte("v1"))
	ae.OnKeyUpdate([]byte("k1"), ss)
	rootAfterUpdate := ae.GetMerkleRoot()

	if bytes.Equal(rootEmpty, rootAfterUpdate) {
		t.Error("OnKeyUpdate did not change the root")
	}

	ae.OnKeyDelete([]byte("k1"), ss)
	rootAfterDelete := ae.GetMerkleRoot()

	if !bytes.Equal(rootEmpty, rootAfterDelete) {
		t.Error("OnKeyDelete did not restore the root")
	}
}
