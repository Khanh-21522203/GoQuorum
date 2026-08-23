package coordinator

import (
	"errors"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
)

func TestWriteRequestFSM_QuorumReachedAndStragglers(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local"})
	mm := membership.NewMembershipManager(membership.Config{NodeID: "local"}, "1.0.0")
	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	c := NewCoordinator("local", ring, st, tr, mm, rt, config.QuorumConfig{N: 3, R: 2, W: 2})

	var resolvedErr error
	resolveCount := 0

	req := c.newWriteRequest(3, 2, "put", func(err error) {
		resolveCount++
		resolvedErr = err
	})

	if req.state != requestAwaiting {
		t.Fatalf("expected initial state requestAwaiting, got %v", req.state)
	}

	// 1st Replica succeeds -> still awaiting
	c.onWriteReplicaResult(req.id, nil, "put")
	if req.state != requestAwaiting {
		t.Fatalf("expected state requestAwaiting after 1st ack, got %v", req.state)
	}
	if resolveCount != 0 {
		t.Errorf("expected 0 resolutions before quorum, got %d", resolveCount)
	}

	// 2nd Replica succeeds -> quorum reached!
	c.onWriteReplicaResult(req.id, nil, "put")
	if req.state != requestSucceeded {
		t.Fatalf("expected state requestSucceeded after 2nd ack, got %v", req.state)
	}
	if resolveCount != 1 {
		t.Fatalf("expected exactly 1 resolution after quorum, got %d", resolveCount)
	}
	if resolvedErr != nil {
		t.Errorf("expected nil error on success, got %v", resolvedErr)
	}

	// 3rd Replica (straggler) arrives -> must NOT trigger resolve again
	c.onWriteReplicaResult(req.id, nil, "put")
	if resolveCount != 1 {
		t.Errorf("expected still 1 resolution after straggler, got %d", resolveCount)
	}
	if req.successCount != 3 {
		t.Errorf("expected successCount = 3, got %d", req.successCount)
	}

	// Request should now be cleaned up from Coordinator map
	if _, exists := c.writeRequests[req.id]; exists {
		t.Error("expected writeRequest to be deleted from map after all replicas arrived")
	}
}

func TestWriteRequestFSM_QuorumUnreachable(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local"})
	mm := membership.NewMembershipManager(membership.Config{NodeID: "local"}, "1.0.0")
	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	c := NewCoordinator("local", ring, st, tr, mm, rt, config.QuorumConfig{N: 3, R: 2, W: 2})

	var resolvedErr error
	resolveCount := 0

	req := c.newWriteRequest(3, 2, "put", func(err error) {
		resolveCount++
		resolvedErr = err
	})

	// 1st Replica fails -> still awaiting (remaining = 2 >= W)
	c.onWriteReplicaResult(req.id, errors.New("conn reset"), "put")
	if req.state != requestAwaiting {
		t.Fatalf("expected state requestAwaiting after 1st failure, got %v", req.state)
	}
	if resolveCount != 0 {
		t.Errorf("expected 0 resolutions, got %d", resolveCount)
	}

	// 2nd Replica fails -> quorum unreachable (remaining = 1 < W)
	c.onWriteReplicaResult(req.id, errors.New("timeout"), "put")
	if req.state != requestFailed {
		t.Fatalf("expected state requestFailed after 2nd failure, got %v", req.state)
	}
	if resolveCount != 1 {
		t.Fatalf("expected 1 resolution, got %d", resolveCount)
	}
	var qErr *quorumerr.QuorumError
	if !errors.As(resolvedErr, &qErr) {
		t.Fatalf("expected QuorumError, got %v", resolvedErr)
	}
	if qErr.Achieved != 0 || qErr.Required != 2 {
		t.Errorf("unexpected quorum error achieved=%d, required=%d", qErr.Achieved, qErr.Required)
	}

	// 3rd Replica arrives -> should not double-resolve
	c.onWriteReplicaResult(req.id, nil, "put")
	if resolveCount != 1 {
		t.Errorf("expected still 1 resolution, got %d", resolveCount)
	}
}

func TestWriteRequestFSM_Timeout(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local"})
	mm := membership.NewMembershipManager(membership.Config{NodeID: "local"}, "1.0.0")
	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	c := NewCoordinator("local", ring, st, tr, mm, rt, config.QuorumConfig{N: 3, R: 2, W: 2})

	var resolvedErr error
	resolveCount := 0

	req := c.newWriteRequest(3, 2, "put", func(err error) {
		resolveCount++
		resolvedErr = err
	})

	// Fire timeout
	c.onWriteTimeout(req.id, "put")
	if req.state != requestFailed {
		t.Fatalf("expected state requestFailed after timeout, got %v", req.state)
	}
	if resolveCount != 1 {
		t.Fatalf("expected 1 resolution on timeout, got %d", resolveCount)
	}
	var qErr *quorumerr.QuorumError
	if !errors.As(resolvedErr, &qErr) {
		t.Fatalf("expected QuorumError on timeout, got %v", resolvedErr)
	}
}

func TestReadRequestFSM_QuorumReachedAndStragglers(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local"})
	mm := membership.NewMembershipManager(membership.Config{NodeID: "local"}, "1.0.0")
	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	c := NewCoordinator("local", ring, st, tr, mm, rt, config.QuorumConfig{N: 3, R: 2, W: 2})

	var resolvedSiblings []adapter.Sibling
	var resolvedErr error
	resolveCount := 0

	req := c.newReadRequest([]byte("k1"), 3, 2, func(s []adapter.Sibling, err error) {
		resolveCount++
		resolvedSiblings = s
		resolvedErr = err
	})

	if req.state != requestAwaiting {
		t.Fatalf("expected initial state requestAwaiting, got %v", req.state)
	}

	vc1 := vclock.NewVectorClock()
	vc1.Set("n1", 1)
	ss1 := &adapter.SiblingSet{
		Siblings: []adapter.Sibling{{Value: []byte("v1"), VClock: vc1, Timestamp: time.Now().Unix()}},
	}

	// 1st Replica succeeds
	c.onReadReplicaResult(req.id, "n1", ss1, nil)
	if req.state != requestAwaiting {
		t.Fatalf("expected state requestAwaiting, got %v", req.state)
	}
	if resolveCount != 0 {
		t.Errorf("expected 0 resolutions before quorum, got %d", resolveCount)
	}

	// 2nd Replica succeeds -> quorum reached!
	vc2 := vclock.NewVectorClock()
	vc2.Set("n2", 1)
	ss2 := &adapter.SiblingSet{
		Siblings: []adapter.Sibling{{Value: []byte("v2"), VClock: vc2, Timestamp: time.Now().Unix()}},
	}

	c.onReadReplicaResult(req.id, "n2", ss2, nil)
	if req.state != requestSucceeded {
		t.Fatalf("expected state requestSucceeded, got %v", req.state)
	}
	if resolveCount != 1 {
		t.Fatalf("expected exactly 1 resolution after quorum, got %d", resolveCount)
	}
	if resolvedErr != nil {
		t.Fatalf("expected nil error, got %v", resolvedErr)
	}
	if len(resolvedSiblings) != 2 {
		t.Fatalf("expected 2 merged siblings, got %d", len(resolvedSiblings))
	}

	// 3rd Replica (straggler) arrives -> must not double-resolve
	c.onReadReplicaResult(req.id, "n3", ss2, nil)
	if resolveCount != 1 {
		t.Errorf("expected still 1 resolution after straggler, got %d", resolveCount)
	}
}

func TestReadRequestFSM_Timeout(t *testing.T) {
	ring := hashring.NewHashRing(64)
	_ = ring.AddNode(&node.Node{ID: "local"})
	mm := membership.NewMembershipManager(membership.Config{NodeID: "local"}, "1.0.0")
	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, "local")
	tr := newFakeTransport(rt)

	c := NewCoordinator("local", ring, st, tr, mm, rt, config.QuorumConfig{N: 3, R: 2, W: 2})

	var resolvedErr error
	resolveCount := 0

	req := c.newReadRequest([]byte("k1"), 3, 2, func(s []adapter.Sibling, err error) {
		resolveCount++
		resolvedErr = err
	})

	// Timeout fires
	c.onReadTimeout(req.id)
	if req.state != requestFailed {
		t.Fatalf("expected state requestFailed on timeout, got %v", req.state)
	}
	if resolveCount != 1 {
		t.Fatalf("expected 1 resolution on timeout, got %d", resolveCount)
	}
	var qErr *quorumerr.QuorumError
	if !errors.As(resolvedErr, &qErr) {
		t.Fatalf("expected QuorumError on timeout, got %v", resolvedErr)
	}
}
