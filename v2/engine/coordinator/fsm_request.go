package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/readrepair"
	"goquorum.io/v2/infra/reactor"
)

// requestState represents the resolution lifecycle of an in-flight quorum request.
//
// Quorum Resolution State Machine:
//
//	                   ┌─── triggerQuorumReached ────> [requestSucceeded]
//	                   │                               (>= W or R acks)
//	[requestAwaiting] ─┼─── triggerQuorumUnreachable ──> [requestFailed]
//	                   │                               (too many failures)
//	                   └─── triggerTimeout ───────────> [requestFailed]
//	                                                   (client deadline)
type requestState int

const (
	requestAwaiting  requestState = iota // Waiting on replica responses.
	requestSucceeded                     // Quorum reached; caller callback completed.
	requestFailed                        // Quorum unreachable or timed out.
)

// requestTrigger is the set of events driving requestState transitions.
type requestTrigger int

const (
	triggerReplicaSuccess requestTrigger = iota // Replica call succeeded.
	triggerReplicaFailure                       // Replica call failed.
	triggerTimeout                              // Client request deadline elapsed.
)

// writeRequest tracks in-flight Put or Delete replica fan-out across N replicas.
type writeRequest struct {
	id           uint64
	total        int // Number of replicas contacted (N).
	quorum       int // Required success count (W).
	successCount int
	failureCount int
	state        requestState
	resolve      func(error) // Invoked once on quorum resolution.
	timerID      reactor.TimerID
}

func newWriteRequest(id uint64, total, quorum int, resolve func(error)) *writeRequest {
	return &writeRequest{
		id:      id,
		total:   total,
		quorum:  quorum,
		state:   requestAwaiting,
		resolve: resolve,
	}
}

func (req *writeRequest) handleResult(err error, op string, cancelTimer func(reactor.TimerID)) {
	switch req.state {
	case requestAwaiting:
		if err == nil {
			req.successCount++
			if req.successCount >= req.quorum {
				req.transitionTo(requestSucceeded, op, cancelTimer)
			}
		} else {
			req.failureCount++
			if req.total-req.failureCount < req.quorum {
				req.transitionTo(requestFailed, op, cancelTimer)
			}
		}
	case requestSucceeded, requestFailed:
		if err == nil {
			req.successCount++
		} else {
			req.failureCount++
		}
	}
}

func (req *writeRequest) handleTimeout(op string, cancelTimer func(reactor.TimerID)) {
	if req.state == requestAwaiting {
		req.transitionTo(requestFailed, op, cancelTimer)
	}
}

func (req *writeRequest) transitionTo(next requestState, op string, cancelTimer func(reactor.TimerID)) {
	req.state = next
	if cancelTimer != nil {
		cancelTimer(req.timerID)
	}
	switch next {
	case requestSucceeded:
		req.resolve(nil)
	case requestFailed:
		req.resolve(newQuorumError(op, req.quorum, req.successCount))
	}
}

func (req *writeRequest) isDone() bool {
	return req.successCount+req.failureCount >= req.total
}

// readRequest tracks in-flight Get replica fan-out across N replicas.
type readRequest struct {
	id           uint64
	key          []byte
	total        int // Number of replicas contacted (N).
	quorum       int // Required success count (R).
	successCount int
	failureCount int
	state        requestState
	responses    []readrepair.ReplicaRead // Collected replica responses in arrival order.
	resolve      func([]adapter.Sibling, error)
	timerID      reactor.TimerID
}

func newReadRequest(id uint64, key []byte, total, quorum int, resolve func([]adapter.Sibling, error)) *readRequest {
	return &readRequest{
		id:      id,
		key:     key,
		total:   total,
		quorum:  quorum,
		state:   requestAwaiting,
		resolve: resolve,
	}
}

func (req *readRequest) handleResult(nodeID node.NodeID, ss *adapter.SiblingSet, err error, repair func(key []byte, merged []adapter.Sibling, responses []readrepair.ReplicaRead), cancelTimer func(reactor.TimerID)) {
	req.responses = append(req.responses, readrepair.ReplicaRead{NodeID: nodeID, SiblingSet: ss, Error: err})

	switch req.state {
	case requestAwaiting:
		if err == nil {
			req.successCount++
			if req.successCount >= req.quorum {
				req.transitionTo(requestSucceeded, repair, cancelTimer)
			}
		} else {
			req.failureCount++
			if req.total-req.failureCount < req.quorum {
				req.transitionTo(requestFailed, repair, cancelTimer)
			}
		}
	case requestSucceeded, requestFailed:
		if err == nil {
			req.successCount++
		} else {
			req.failureCount++
		}
	}
}

func (req *readRequest) handleTimeout(repair func(key []byte, merged []adapter.Sibling, responses []readrepair.ReplicaRead), cancelTimer func(reactor.TimerID)) {
	if req.state == requestAwaiting {
		req.transitionTo(requestFailed, repair, cancelTimer)
	}
}

func (req *readRequest) transitionTo(next requestState, repair func(key []byte, merged []adapter.Sibling, responses []readrepair.ReplicaRead), cancelTimer func(reactor.TimerID)) {
	req.state = next
	if cancelTimer != nil {
		cancelTimer(req.timerID)
	}
	switch next {
	case requestSucceeded:
		merged := mergeMaximalSiblings(req.responses)
		if repair != nil {
			repair(req.key, merged, req.responses)
		}
		req.resolve(visibleSiblings(merged), nil)
	case requestFailed:
		req.resolve(nil, newQuorumError("get", req.quorum, req.successCount))
	}
}

func (req *readRequest) isDone() bool {
	return req.successCount+req.failureCount >= req.total
}

func mergeMaximalSiblings(responses []readrepair.ReplicaRead) []adapter.Sibling {
	var all []adapter.Sibling
	for _, r := range responses {
		if r.Error != nil || r.SiblingSet == nil {
			continue
		}
		all = append(all, r.SiblingSet.Siblings...)
	}

	maximal := make([]adapter.Sibling, 0, len(all))
	for i, s := range all {
		dominated := false
		for j, other := range all {
			if i == j {
				continue
			}
			if other.VClock.Dominates(s.VClock) && !other.VClock.Equals(s.VClock) {
				dominated = true
				break
			}
		}
		if dominated {
			continue
		}
		duplicate := false
		for _, m := range maximal {
			if m.VClock.Equals(s.VClock) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			maximal = append(maximal, s)
		}
	}
	return maximal
}

func visibleSiblings(merged []adapter.Sibling) []adapter.Sibling {
	now := time.Now().Unix()
	visible := make([]adapter.Sibling, 0, len(merged))
	for _, s := range merged {
		if s.Tombstone {
			continue
		}
		if s.ExpiresAt != 0 && s.ExpiresAt <= now {
			continue
		}
		visible = append(visible, s)
	}
	return visible
}

func newQuorumError(op string, required, achieved int) error {
	return &quorumerr.QuorumError{
		Type:      quorumerr.QuorumNotReached,
		Required:  required,
		Achieved:  achieved,
		Operation: op,
	}
}
