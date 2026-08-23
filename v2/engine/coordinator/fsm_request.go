package coordinator

import (
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/readrepair"
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
	triggerReplicaSuccess    requestTrigger = iota // Replica call succeeded.
	triggerReplicaFailure                          // Replica call failed.
	triggerQuorumReached                           // Success count reached required quorum.
	triggerQuorumUnreachable                       // Remaining replicas cannot achieve quorum.
	triggerTimeout                                 // Client request deadline elapsed.
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

func (c *Coordinator) newWriteRequest(total, quorum int, op string, resolve func(error)) *writeRequest {
	req := &writeRequest{
		id:      c.nextRequestID(),
		total:   total,
		quorum:  quorum,
		state:   requestAwaiting,
		resolve: resolve,
	}
	c.writeRequests[req.id] = req
	req.timerID = c.reactor.ScheduleOnce(c.timeoutConfig.ClientTimeout, func() {
		c.onWriteTimeout(req.id, op)
	})
	return req
}

func (c *Coordinator) onWriteReplicaResult(reqID uint64, err error, op string) {
	req, ok := c.writeRequests[reqID]
	if !ok {
		return
	}

	if err == nil {
		c.handleWriteRequest(req, triggerReplicaSuccess, op)
	} else {
		c.handleWriteRequest(req, triggerReplicaFailure, op)
	}

	if req.successCount+req.failureCount >= req.total {
		delete(c.writeRequests, req.id)
	}
}

func (c *Coordinator) onWriteTimeout(reqID uint64, op string) {
	req, ok := c.writeRequests[reqID]
	if !ok || req.state != requestAwaiting {
		return
	}
	delete(c.writeRequests, reqID)
	c.handleWriteRequest(req, triggerTimeout, op)
}

func (c *Coordinator) handleWriteRequest(req *writeRequest, trigger requestTrigger, op string) {
	switch req.state {
	case requestAwaiting:
		switch trigger {
		case triggerReplicaSuccess:
			req.successCount++
			if req.successCount >= req.quorum {
				c.transitionWriteRequest(req, requestSucceeded, op)
			}
		case triggerReplicaFailure:
			req.failureCount++
			if req.total-req.failureCount < req.quorum {
				c.transitionWriteRequest(req, requestFailed, op)
			}
		case triggerTimeout:
			c.transitionWriteRequest(req, requestFailed, op)
		}

	case requestSucceeded, requestFailed:
		switch trigger {
		case triggerReplicaSuccess:
			req.successCount++
		case triggerReplicaFailure:
			req.failureCount++
		}
	}
}

func (c *Coordinator) transitionWriteRequest(req *writeRequest, next requestState, op string) {
	req.state = next
	c.enterWriteRequestState(req, next, op)
}

func (c *Coordinator) enterWriteRequestState(req *writeRequest, s requestState, op string) {
	switch s {
	case requestSucceeded:
		c.reactor.CancelTimer(req.timerID)
		req.resolve(nil)
	case requestFailed:
		c.reactor.CancelTimer(req.timerID)
		req.resolve(newQuorumError(op, req.quorum, req.successCount))
	}
}

func (c *Coordinator) newReadRequest(key []byte, total, quorum int, resolve func([]adapter.Sibling, error)) *readRequest {
	req := &readRequest{
		id:      c.nextRequestID(),
		key:     key,
		total:   total,
		quorum:  quorum,
		state:   requestAwaiting,
		resolve: resolve,
	}
	c.readRequests[req.id] = req
	req.timerID = c.reactor.ScheduleOnce(c.timeoutConfig.ClientTimeout, func() {
		c.onReadTimeout(req.id)
	})
	return req
}

func (c *Coordinator) onReadReplicaResult(reqID uint64, nodeID node.NodeID, ss *adapter.SiblingSet, err error) {
	req, ok := c.readRequests[reqID]
	if !ok {
		return
	}

	req.responses = append(req.responses, readrepair.ReplicaRead{NodeID: nodeID, SiblingSet: ss, Error: err})

	if err == nil {
		c.handleReadRequest(req, triggerReplicaSuccess)
	} else {
		c.handleReadRequest(req, triggerReplicaFailure)
	}

	if req.successCount+req.failureCount >= req.total {
		delete(c.readRequests, req.id)
	}
}

func (c *Coordinator) onReadTimeout(reqID uint64) {
	req, ok := c.readRequests[reqID]
	if !ok || req.state != requestAwaiting {
		return
	}
	delete(c.readRequests, reqID)
	c.handleReadRequest(req, triggerTimeout)
}

func (c *Coordinator) handleReadRequest(req *readRequest, trigger requestTrigger) {
	switch req.state {
	case requestAwaiting:
		switch trigger {
		case triggerReplicaSuccess:
			req.successCount++
			if req.successCount >= req.quorum {
				c.transitionReadRequest(req, requestSucceeded)
			}
		case triggerReplicaFailure:
			req.failureCount++
			if req.total-req.failureCount < req.quorum {
				c.transitionReadRequest(req, requestFailed)
			}
		case triggerTimeout:
			c.transitionReadRequest(req, requestFailed)
		}

	case requestSucceeded, requestFailed:
		switch trigger {
		case triggerReplicaSuccess:
			req.successCount++
		case triggerReplicaFailure:
			req.failureCount++
		}
	}
}

func (c *Coordinator) transitionReadRequest(req *readRequest, next requestState) {
	req.state = next
	c.enterReadRequestState(req, next)
}

func (c *Coordinator) enterReadRequestState(req *readRequest, s requestState) {
	switch s {
	case requestSucceeded:
		c.reactor.CancelTimer(req.timerID)
		merged := mergeMaximalSiblings(req.responses)
		c.readRepairer.TriggerRepair(req.key, merged, req.responses)
		req.resolve(visibleSiblings(merged), nil)
	case requestFailed:
		c.reactor.CancelTimer(req.timerID)
		req.resolve(nil, newQuorumError("get", req.quorum, req.successCount))
	}
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
