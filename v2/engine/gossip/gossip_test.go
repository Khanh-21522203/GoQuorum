package gossip

import (
	"sync"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter/storage"
	"goquorum.io/v2/engine/adapter/transport"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
)

// fakeSource is a minimal, controllable reactor.EventSource: Poll blocks
// until Wake is called or the deadline passes.
type fakeSource struct {
	mu   sync.Mutex
	wake chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{wake: make(chan struct{}, 1)}
}

func (f *fakeSource) Poll(dst []reactor.Event, deadline time.Time) ([]reactor.Event, error) {
	if deadline.IsZero() {
		return dst, nil
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return dst, nil
	}
	select {
	case <-f.wake:
	case <-time.After(wait):
	}
	return dst, nil
}

func (f *fakeSource) Wake() error {
	select {
	case f.wake <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeSource) Close() error { return nil }

// runInBackground drives r.Run on its own goroutine for the duration of a
// test, matching the pattern engine/reactor's own tests use: only test code
// spans goroutines, product code stays single-threaded.
func runInBackground(t *testing.T, r *reactor.Reactor) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- r.Run() }()
	t.Cleanup(func() {
		r.RequestStop()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after RequestStop")
		}
	})
}

// fakeTransport is a transport.Transport where only GossipExchange has
// configurable behavior; every other method reports success immediately.
type fakeTransport struct {
	mu          sync.Mutex
	exchangeFn  func(id node.NodeID, entries []transport.GossipEntry) ([]transport.GossipEntry, error)
	exchangeCnt int
}

func (f *fakeTransport) RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error)) {
	done(nil)
}

func (f *fakeTransport) RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, nil)
}

func (f *fakeTransport) Heartbeat(id node.NodeID, done func(error)) {
	done(nil)
}

func (f *fakeTransport) GetMerkleRoot(id node.NodeID, done func([]byte, error)) {
	done(nil, nil)
}

func (f *fakeTransport) NotifyLeaving(id node.NodeID, done func(error)) {
	done(nil)
}

func (f *fakeTransport) GossipExchange(id node.NodeID, entries []transport.GossipEntry, done func([]transport.GossipEntry, error)) {
	f.mu.Lock()
	f.exchangeCnt++
	fn := f.exchangeFn
	f.mu.Unlock()
	if fn == nil {
		done(nil, nil)
		return
	}
	reply, err := fn(id, entries)
	done(reply, err)
}

func (f *fakeTransport) Close() error { return nil }

func (f *fakeTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchangeCnt
}

func newMembershipWithPeer(t *testing.T, local node.NodeID, peer node.NodeID, peerHTTPAddr string) *membership.MembershipManager {
	t.Helper()
	mm := membership.NewMembershipManager(membership.Config{
		NodeID:     local,
		ListenAddr: "local:1",
		Members: []membership.MemberConfig{
			{ID: local, Addr: "local:1", HTTPAddr: "local:1"},
			{ID: peer, Addr: "peer:1", HTTPAddr: peerHTTPAddr},
		},
	}, "test")
	mm.AddPeer(peer, "peer:1", peerHTTPAddr)
	return mm
}

func TestMerge_LastWriterWins(t *testing.T) {
	mm := newMembershipWithPeer(t, "local", "peer-1", "peer-1:http")
	g := NewGossip("local", "local:http", mm, &fakeTransport{}, reactor.New(newFakeSource()), GossipConfig{})

	g.state["peer-1"] = &NodeEntry{NodeID: "peer-1", Status: membership.NodeStatusActive, Version: 1, UpdatedAt: 100}

	// Older UpdatedAt loses.
	g.Merge(map[node.NodeID]*NodeEntry{
		"peer-1": {NodeID: "peer-1", Status: membership.NodeStatusSuspect, Version: 2, UpdatedAt: 50},
	})
	if got := g.state["peer-1"].Status; got != membership.NodeStatusActive {
		t.Fatalf("older entry must not overwrite: got status %v", got)
	}

	// Newer UpdatedAt wins.
	g.Merge(map[node.NodeID]*NodeEntry{
		"peer-1": {NodeID: "peer-1", Status: membership.NodeStatusSuspect, Version: 2, UpdatedAt: 150},
	})
	if got := g.state["peer-1"].Status; got != membership.NodeStatusSuspect {
		t.Fatalf("newer entry must overwrite: got status %v", got)
	}
	if got := mm.GetPeerStatus("peer-1"); got != membership.NodeStatusSuspect {
		t.Fatalf("membership manager not updated on merge: got %v", got)
	}

	// The local node's own entry is never overwritten by an incoming one.
	selfBefore := *g.state["local"]
	g.Merge(map[node.NodeID]*NodeEntry{
		"local": {NodeID: "local", Status: membership.NodeStatusFailed, Version: 99, UpdatedAt: selfBefore.UpdatedAt + 1000},
	})
	if got := g.state["local"]; got.Status != selfBefore.Status || got.Version != selfBefore.Version {
		t.Fatalf("local entry was overwritten by incoming gossip: %+v", got)
	}
}

func TestMarkPeer_BumpsUpdatedAt(t *testing.T) {
	mm := newMembershipWithPeer(t, "local", "peer-1", "peer-1:http")
	g := NewGossip("local", "local:http", mm, &fakeTransport{}, reactor.New(newFakeSource()), GossipConfig{})

	g.MarkPeer("peer-1", membership.NodeStatusFailed)
	entry, ok := g.state["peer-1"]
	if !ok {
		t.Fatal("MarkPeer did not create an entry for an unseen peer")
	}
	if entry.Status != membership.NodeStatusFailed {
		t.Fatalf("expected status Failed, got %v", entry.Status)
	}
	if entry.UpdatedAt == 0 {
		t.Fatal("MarkPeer did not stamp UpdatedAt")
	}
}

func TestSetSelf_BumpsVersionAndUpdatedAt(t *testing.T) {
	mm := newMembershipWithPeer(t, "local", "peer-1", "peer-1:http")
	g := NewGossip("local", "local:http", mm, &fakeTransport{}, reactor.New(newFakeSource()), GossipConfig{})

	before := *g.state["local"]
	g.state["local"].UpdatedAt = before.UpdatedAt - 10 // Force a detectable advance without sleeping.
	g.SetSelf(membership.NodeStatusLeaving)

	after := g.state["local"]
	if after.Version != before.Version+1 {
		t.Fatalf("expected version %d, got %d", before.Version+1, after.Version)
	}
	if after.Status != membership.NodeStatusLeaving {
		t.Fatalf("expected status Leaving, got %v", after.Status)
	}
	if after.UpdatedAt <= before.UpdatedAt-10 {
		t.Fatalf("expected UpdatedAt to advance past %d, got %d", before.UpdatedAt-10, after.UpdatedAt)
	}
}

func TestGetState_ReturnsDefensiveCopy(t *testing.T) {
	mm := newMembershipWithPeer(t, "local", "peer-1", "peer-1:http")
	g := NewGossip("local", "local:http", mm, &fakeTransport{}, reactor.New(newFakeSource()), GossipConfig{})

	snapshot := g.GetState()
	snapshot["local"].Status = membership.NodeStatusFailed

	if g.state["local"].Status == membership.NodeStatusFailed {
		t.Fatal("mutating GetState's result mutated internal state")
	}
}

func TestStart_RunsRoundAndMergesReply(t *testing.T) {
	mm := newMembershipWithPeer(t, "local", "peer-1", "peer-1:http")

	replied := make(chan struct{}, 1)
	ft := &fakeTransport{
		exchangeFn: func(id node.NodeID, entries []transport.GossipEntry) ([]transport.GossipEntry, error) {
			reply := []transport.GossipEntry{
				{NodeID: "peer-2", Status: uint8(membership.NodeStatusActive), Version: 1, UpdatedAt: time.Now().Unix() + 1000},
			}
			select {
			case replied <- struct{}{}:
			default:
			}
			return reply, nil
		},
	}

	rt := reactor.New(newFakeSource())
	g := NewGossip("local", "local:http", mm, ft, rt, GossipConfig{Enabled: true, FanOut: 1, Interval: 10 * time.Millisecond})
	// Start (which arms the reactor timer) must happen before Run begins,
	// since ScheduleEvery is only safe to call from the reactor's own
	// goroutine; the goroutine created by runInBackground below does not
	// exist yet, so there is no other goroutine to race with.
	g.Start()
	runInBackground(t, rt)

	select {
	case <-replied:
	case <-time.After(time.Second):
		t.Fatal("gossip round never reached the transport")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		done := make(chan map[node.NodeID]*NodeEntry, 1)
		rt.PostFunc(func() { done <- g.GetState() })
		state := <-done
		if _, ok := state["peer-2"]; ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reply from GossipExchange was never merged into GetState")
}

func TestStop_PreventsFurtherRounds(t *testing.T) {
	mm := newMembershipWithPeer(t, "local", "peer-1", "peer-1:http")
	ft := &fakeTransport{}

	rt := reactor.New(newFakeSource())
	g := NewGossip("local", "local:http", mm, ft, rt, GossipConfig{Enabled: true, FanOut: 1, Interval: 10 * time.Millisecond})
	// Start before Run begins, for the same reason as in
	// TestStart_RunsRoundAndMergesReply: no reactor goroutine exists yet to
	// race with.
	g.Start()
	runInBackground(t, rt)

	// Let a few rounds happen.
	deadline := time.Now().Add(500 * time.Millisecond)
	for ft.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ft.count() < 1 {
		t.Fatal("no gossip round ran before Stop")
	}

	done := make(chan struct{})
	rt.PostFunc(func() {
		g.Stop()
		close(done)
	})
	<-done

	countAfterStop := ft.count()
	time.Sleep(100 * time.Millisecond)
	if got := ft.count(); got != countAfterStop {
		t.Fatalf("gossip round ran after Stop: count went from %d to %d", countAfterStop, got)
	}
}
