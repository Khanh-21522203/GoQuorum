package coordinator

import (
	"errors"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
)

func TestPeerFSM_TransitionsAndRecovery(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local", State: node.NodeStateActive})
	_ = ring.AddNode(&node.Node{ID: "peer-1", State: node.NodeStateActive})

	mm := membership.NewMembershipManager(membership.Config{NodeID: "local", ListenAddr: "127.0.0.1:8000"}, "1.0.0")
	mm.AddPeer("peer-1", "127.0.0.1:8001", "127.0.0.1:8002")

	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	cfg := config.DefaultQuorumConfig()
	fdCfg := config.FailureDetectorConfig{
		HeartbeatInterval: 0,
		FailureThreshold:  3,
	}

	c := NewCoordinator("local", ring, st, tr, mm, rt, cfg, WithFailureDetectorConfig(fdCfg))

	peer := c.peers["peer-1"]
	if peer.state != node.NodeStateActive {
		t.Fatalf("expected initial state Active, got %v", peer.state)
	}

	// Miss 1 -> Degraded
	c.OnHeartbeatResult("peer-1", errors.New("timeout"))
	if peer.state != node.NodeStateDegraded {
		t.Fatalf("expected state Degraded after 1 miss, got %v", peer.state)
	}
	if mm.GetClusterView()["peer-1"] != membership.NodeStatusSuspect {
		t.Errorf("expected membership status Suspect, got %v", mm.GetClusterView()["peer-1"])
	}
	n, _ := ring.GetNode("peer-1")
	if n.GetState() != node.NodeStateDegraded {
		t.Errorf("expected ring node state Degraded, got %v", n.GetState())
	}

	// Miss 2 -> Degraded
	c.OnHeartbeatResult("peer-1", errors.New("timeout"))
	if peer.state != node.NodeStateDegraded {
		t.Fatalf("expected state Degraded after 2 misses, got %v", peer.state)
	}
	if peer.misses != 2 {
		t.Errorf("expected 2 misses, got %d", peer.misses)
	}

	// Miss 3 (threshold = 3) -> Failed
	c.OnHeartbeatResult("peer-1", errors.New("timeout"))
	if peer.state != node.NodeStateFailed {
		t.Fatalf("expected state Failed after 3 misses, got %v", peer.state)
	}
	if mm.GetClusterView()["peer-1"] != membership.NodeStatusFailed {
		t.Errorf("expected membership status Failed, got %v", mm.GetClusterView()["peer-1"])
	}
	if n.GetState() != node.NodeStateFailed {
		t.Errorf("expected ring node state Failed, got %v", n.GetState())
	}

	// Store a hint while failed
	c.handoff.StoreHint("peer-1", []byte("k1"), &adapter.SiblingSet{
		Siblings: []adapter.Sibling{{Value: []byte("v1")}},
	})
	if c.handoff.HintCount("peer-1") != 1 {
		t.Fatalf("expected 1 hint stored for peer-1, got %d", c.handoff.HintCount("peer-1"))
	}

	// Heartbeat OK -> Recovers to Active and flushes hint
	c.OnHeartbeatResult("peer-1", nil)
	if peer.state != node.NodeStateActive {
		t.Fatalf("expected state Active after recovery, got %v", peer.state)
	}
	if peer.misses != 0 {
		t.Errorf("expected 0 misses after recovery, got %d", peer.misses)
	}
	if mm.GetClusterView()["peer-1"] != membership.NodeStatusActive {
		t.Errorf("expected membership status Active, got %v", mm.GetClusterView()["peer-1"])
	}
	if n.GetState() != node.NodeStateActive {
		t.Errorf("expected ring node state Active, got %v", n.GetState())
	}
	if c.handoff.HintCount("peer-1") != 0 {
		t.Errorf("expected hints to be flushed upon recovery, remaining: %d", c.handoff.HintCount("peer-1"))
	}
}

func TestPeerFSM_OnGossipReceived(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local", State: node.NodeStateActive})
	_ = ring.AddNode(&node.Node{ID: "peer-1", State: node.NodeStateActive})

	mm := membership.NewMembershipManager(membership.Config{NodeID: "local", ListenAddr: "127.0.0.1:8000"}, "1.0.0")
	mm.AddPeer("peer-1", "127.0.0.1:8001", "127.0.0.1:8002")

	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	c := NewCoordinator("local", ring, st, tr, mm, rt, config.DefaultQuorumConfig())

	entries := []adapter.GossipEntry{
		{
			NodeID: "peer-1",
			Addr:   "127.0.0.1:8001",
			Status: uint8(membership.NodeStatusSuspect),
		},
	}

	c.OnGossipReceived("peer-1", entries)

	if mm.GetClusterView()["peer-1"] != membership.NodeStatusSuspect {
		t.Errorf("expected membership status Suspect after gossip, got %v", mm.GetClusterView()["peer-1"])
	}
	n, _ := ring.GetNode("peer-1")
	if n.GetState() != node.NodeStateDegraded {
		t.Errorf("expected ring node state Degraded after gossip, got %v", n.GetState())
	}
}
