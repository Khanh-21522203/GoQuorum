package coordinator

import (
	"errors"
	"testing"
	"time"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/readrepair"
)

func TestWriteRequestFSM_QuorumReachedAndStragglers(t *testing.T) {
	var resolvedErr error
	resolveCount := 0
	timerCancelled := false

	req := newWriteRequest(101, 3, 2, func(err error) {
		resolveCount++
		resolvedErr = err
	})
	req.timerID = 1

	cancelTimer := func(id reactor.TimerID) {
		timerCancelled = true
	}

	if req.state != requestAwaiting {
		t.Fatalf("expected initial state requestAwaiting, got %v", req.state)
	}

	// 1st Replica succeeds -> still awaiting
	req.handleResult(nil, "put", cancelTimer)
	if req.state != requestAwaiting {
		t.Fatalf("expected state requestAwaiting after 1st ack, got %v", req.state)
	}
	if resolveCount != 0 {
		t.Errorf("expected 0 resolutions before quorum, got %d", resolveCount)
	}

	// 2nd Replica succeeds -> quorum reached!
	req.handleResult(nil, "put", cancelTimer)
	if req.state != requestSucceeded {
		t.Fatalf("expected state requestSucceeded after 2nd ack, got %v", req.state)
	}
	if resolveCount != 1 {
		t.Fatalf("expected exactly 1 resolution after quorum, got %d", resolveCount)
	}
	if resolvedErr != nil {
		t.Errorf("expected nil error on success, got %v", resolvedErr)
	}
	if !timerCancelled {
		t.Error("expected timer to be cancelled on quorum reached")
	}

	// 3rd Replica (straggler) arrives -> must NOT trigger resolve again
	req.handleResult(nil, "put", cancelTimer)
	if resolveCount != 1 {
		t.Errorf("expected still 1 resolution after straggler, got %d", resolveCount)
	}
	if req.successCount != 3 {
		t.Errorf("expected successCount = 3, got %d", req.successCount)
	}
	if !req.isDone() {
		t.Error("expected req.isDone() to be true when all replicas arrived")
	}
}

func TestWriteRequestFSM_QuorumUnreachable(t *testing.T) {
	var resolvedErr error
	resolveCount := 0

	req := newWriteRequest(102, 3, 2, func(err error) {
		resolveCount++
		resolvedErr = err
	})

	// 1st Replica fails -> still awaiting (remaining = 2 >= W)
	req.handleResult(errors.New("conn reset"), "put", nil)
	if req.state != requestAwaiting {
		t.Fatalf("expected state requestAwaiting after 1st failure, got %v", req.state)
	}
	if resolveCount != 0 {
		t.Errorf("expected 0 resolutions, got %d", resolveCount)
	}

	// 2nd Replica fails -> quorum unreachable (remaining = 1 < W)
	req.handleResult(errors.New("timeout"), "put", nil)
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
	req.handleResult(nil, "put", nil)
	if resolveCount != 1 {
		t.Errorf("expected still 1 resolution, got %d", resolveCount)
	}
}

func TestWriteRequestFSM_Timeout(t *testing.T) {
	var resolvedErr error
	resolveCount := 0

	req := newWriteRequest(103, 3, 2, func(err error) {
		resolveCount++
		resolvedErr = err
	})

	// Fire timeout
	req.handleTimeout("put", nil)
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

func TestReadRequestFSM_QuorumReachedAndRepair(t *testing.T) {
	var resolvedSiblings []adapter.Sibling
	var resolvedErr error
	resolveCount := 0
	repairTriggered := false

	req := newReadRequest(104, []byte("k1"), 3, 2, func(s []adapter.Sibling, err error) {
		resolveCount++
		resolvedSiblings = s
		resolvedErr = err
	})

	repairFunc := func(key []byte, merged []adapter.Sibling, responses []readrepair.ReplicaRead) {
		repairTriggered = true
	}

	if req.state != requestAwaiting {
		t.Fatalf("expected initial state requestAwaiting, got %v", req.state)
	}

	vc1 := vclock.NewVectorClock()
	vc1.Set("n1", 1)
	ss1 := &adapter.SiblingSet{
		Siblings: []adapter.Sibling{{Value: []byte("v1"), VClock: vc1, Timestamp: time.Now().Unix()}},
	}

	// 1st Replica succeeds
	req.handleResult("n1", ss1, nil, repairFunc, nil)
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

	req.handleResult("n2", ss2, nil, repairFunc, nil)
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
	if !repairTriggered {
		t.Error("expected read-repair to be triggered")
	}

	// 3rd Replica (straggler) arrives -> must not double-resolve
	req.handleResult("n3", ss2, nil, repairFunc, nil)
	if resolveCount != 1 {
		t.Errorf("expected still 1 resolution after straggler, got %d", resolveCount)
	}
}

func TestReadRequestFSM_Timeout(t *testing.T) {
	var resolvedErr error
	resolveCount := 0

	req := newReadRequest(105, []byte("k1"), 3, 2, func(s []adapter.Sibling, err error) {
		resolveCount++
		resolvedErr = err
	})

	// Timeout fires
	req.handleTimeout(nil, nil)
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
