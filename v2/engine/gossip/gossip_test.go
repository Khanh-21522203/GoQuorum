package gossip

import (
	"sync"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

type fakeTransport struct {
	mu          sync.Mutex
	exchangeFn  func(id node.NodeID, entries []adapter.GossipEntry) ([]adapter.GossipEntry, error)
	exchangeCnt int
}

func (f *fakeTransport) RemotePut(node.NodeID, uint64, []byte, *adapter.SiblingSet) error {
	return nil
}
func (f *fakeTransport) RemoteGet(node.NodeID, uint64, []byte) error {
	return nil
}
func (f *fakeTransport) Heartbeat(node.NodeID, uint64) error {
	return nil
}
func (f *fakeTransport) GetMerkleRoot(node.NodeID, uint64) error {
	return nil
}
func (f *fakeTransport) NotifyLeaving(node.NodeID, uint64) error {
	return nil
}
func (f *fakeTransport) GossipExchange(id node.NodeID, corrID uint64, entries []adapter.GossipEntry) error {
	f.mu.Lock()
	f.exchangeCnt++
	f.mu.Unlock()
	return nil
}
func (f *fakeTransport) Dial(id node.NodeID, addr string) error { return nil }
func (f *fakeTransport) Close() error                           { return nil }

type testGossipHandler struct {
	mu       sync.Mutex
	received map[node.NodeID][]adapter.GossipEntry
}

func (h *testGossipHandler) OnGossipReceived(peerID node.NodeID, entries []adapter.GossipEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.received[peerID] = entries
}

func TestGossip_Round(t *testing.T) {
	ft := &fakeTransport{
		exchangeFn: func(id node.NodeID, entries []adapter.GossipEntry) ([]adapter.GossipEntry, error) {
			return []adapter.GossipEntry{
				{NodeID: "peer-2", Status: 1, Version: 1, UpdatedAt: 123},
			}, nil
		},
	}

	handler := &testGossipHandler{received: make(map[node.NodeID][]adapter.GossipEntry)}
	g := NewGossip(ft, handler, GossipConfig{FanOut: 2})

	localEntries := []adapter.GossipEntry{
		{NodeID: "local", Status: 1, Version: 1, UpdatedAt: 100},
	}
	peers := []node.NodeID{"peer-1", "peer-2"}

	g.Round(peers, localEntries)

	if ft.exchangeCnt != 2 {
		t.Fatalf("expected 2 exchange calls for 2 peers, got %d", ft.exchangeCnt)
	}
}
