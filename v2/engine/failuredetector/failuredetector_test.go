package failuredetector

import (
	"errors"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

type fakeTransport struct {
	calls map[node.NodeID]int
	errs  map[node.NodeID]error
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		calls: make(map[node.NodeID]int),
		errs:  make(map[node.NodeID]error),
	}
}

func (ft *fakeTransport) Heartbeat(id node.NodeID, done func(error)) {
	ft.calls[id]++
	done(ft.errs[id])
}

func (ft *fakeTransport) RemotePut(node.NodeID, []byte, *adapter.SiblingSet, func(error)) {
	panic("not used")
}
func (ft *fakeTransport) RemoteGet(node.NodeID, []byte, func(*adapter.SiblingSet, error)) {
	panic("not used")
}
func (ft *fakeTransport) GetMerkleRoot(node.NodeID, func([]byte, error)) {
	panic("not used")
}
func (ft *fakeTransport) NotifyLeaving(node.NodeID, func(error)) {
	panic("not used")
}
func (ft *fakeTransport) GossipExchange(node.NodeID, []adapter.GossipEntry, func([]adapter.GossipEntry, error)) {
	panic("not used")
}
func (ft *fakeTransport) Dial(node.NodeID, string) error { return nil }
func (ft *fakeTransport) Close() error                   { return nil }

var _ adapter.Transport = (*fakeTransport)(nil)

type testProbeHandler struct {
	results map[node.NodeID]error
}

func (h *testProbeHandler) OnHeartbeatResult(nodeID node.NodeID, err error) {
	h.results[nodeID] = err
}

func TestFailureDetector_Probe(t *testing.T) {
	ft := newFakeTransport()
	peer1 := node.NodeID("peer-1")
	peer2 := node.NodeID("peer-2")
	ft.errs[peer2] = errors.New("timeout")

	handler := &testProbeHandler{results: make(map[node.NodeID]error)}
	fd := NewFailureDetector(ft, handler)

	fd.Probe([]node.NodeID{peer1, peer2})

	if ft.calls[peer1] != 1 || ft.calls[peer2] != 1 {
		t.Fatalf("expected 1 heartbeat call per peer, got %v", ft.calls)
	}

	if handler.results[peer1] != nil {
		t.Errorf("expected peer1 success, got %v", handler.results[peer1])
	}
	if handler.results[peer2] == nil || handler.results[peer2].Error() != "timeout" {
		t.Errorf("expected peer2 timeout error, got %v", handler.results[peer2])
	}
}

func TestFailureDetector_ProbeOne(t *testing.T) {
	ft := newFakeTransport()
	peer := node.NodeID("peer-1")
	handler := &testProbeHandler{results: make(map[node.NodeID]error)}
	fd := NewFailureDetector(ft, nil)
	fd.SetHandler(handler)

	fd.ProbeOne(peer)

	if ft.calls[peer] != 1 {
		t.Fatalf("expected 1 call, got %d", ft.calls[peer])
	}
	if handler.results[peer] != nil {
		t.Errorf("expected peer success, got %v", handler.results[peer])
	}
}
