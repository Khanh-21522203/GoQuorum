package coordinator

import (
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
)

func TestLifecycleFSM_StartAndStop(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local", State: node.NodeStateActive})
	_ = ring.AddNode(&node.Node{ID: "peer-1", State: node.NodeStateActive})

	mm := membership.NewMembershipManager(membership.Config{NodeID: "local", ListenAddr: "127.0.0.1:8000"}, "1.0.0")
	mm.AddPeer("peer-1", "127.0.0.1:8001", "127.0.0.1:8002")

	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	fdCfg := config.FailureDetectorConfig{HeartbeatInterval: 100 * time.Millisecond, FailureThreshold: 3}
	aeCfg := config.AntiEntropyConfig{Enabled: true, ScanInterval: 200 * time.Millisecond}

	c := NewCoordinator("local", ring, st, tr, mm, rt, config.DefaultQuorumConfig(),
		WithFailureDetectorConfig(fdCfg),
		WithAntiEntropyConfig(aeCfg),
	)

	if c.state != coordinatorNotStarted {
		t.Fatalf("expected initial state NotStarted, got %v", c.state)
	}

	if err := c.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if c.state != coordinatorRunning {
		t.Fatalf("expected state Running after Start, got %v", c.state)
	}
	if c.heartbeatTimer == 0 {
		t.Error("expected heartbeatTimer to be armed")
	}
	if c.antiEntropyTimer == 0 {
		t.Error("expected antiEntropyTimer to be armed")
	}

	c.Stop()

	if c.state != coordinatorStopped {
		t.Fatalf("expected state Stopped after Stop, got %v", c.state)
	}
}
