package failuredetector

import (
	"errors"
	"sync"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/reactor"
)

// fakeSource is a minimal EventSource good enough to drive a Reactor in
// tests: Poll just waits for a Wake or for its deadline, since these tests
// never push real Events, only rely on timers and PostFunc.
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

// runReactor starts r.Run on a background goroutine and stops it, asserting
// a clean shutdown, when the test ends.
func runReactor(t *testing.T, r *reactor.Reactor) {
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

// fakeTransport is a controllable transport.Transport whose Heartbeat
// result per node.NodeID is queued by the test and consumed in FIFO order,
// one entry per call. Once a peer's queue runs dry, Heartbeat keeps
// repeating that peer's most recent result (nil/success for a peer nothing
// was ever queued for) instead of silently switching to success — a state
// like Failed only holds for one HeartbeatInterval before the next tick
// resolves it, so a test asserting on it needs that state to stay put
// rather than flip on whichever tick happens to land after the queue empties.
// Every other transport.Transport method panics: these tests never exercise
// them.
type fakeTransport struct {
	mu    sync.Mutex
	queue map[node.NodeID][]error
	last  map[node.NodeID]error
	calls map[node.NodeID]int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		queue: make(map[node.NodeID][]error),
		last:  make(map[node.NodeID]error),
		calls: make(map[node.NodeID]int),
	}
}

// enqueue appends n more results for id's next n Heartbeat calls.
func (ft *fakeTransport) enqueue(id node.NodeID, results ...error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.queue[id] = append(ft.queue[id], results...)
}

func (ft *fakeTransport) callCount(id node.NodeID) int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.calls[id]
}

func (ft *fakeTransport) Heartbeat(id node.NodeID, done func(error)) {
	ft.mu.Lock()
	ft.calls[id]++
	var err error
	if q := ft.queue[id]; len(q) > 0 {
		err = q[0]
		ft.queue[id] = q[1:]
		ft.last[id] = err
	} else {
		err = ft.last[id]
	}
	ft.mu.Unlock()
	done(err)
}

func (ft *fakeTransport) RemotePut(node.NodeID, []byte, *adapter.SiblingSet, func(error)) {
	panic("not used by these tests")
}
func (ft *fakeTransport) RemoteGet(node.NodeID, []byte, func(*adapter.SiblingSet, error)) {
	panic("not used by these tests")
}
func (ft *fakeTransport) GetMerkleRoot(node.NodeID, func([]byte, error)) {
	panic("not used by these tests")
}
func (ft *fakeTransport) NotifyLeaving(node.NodeID, func(error)) {
	panic("not used by these tests")
}
func (ft *fakeTransport) GossipExchange(node.NodeID, []adapter.GossipEntry, func([]adapter.GossipEntry, error)) {
	panic("not used by these tests")
}
func (ft *fakeTransport) Dial(node.NodeID, string) error { return nil }
func (ft *fakeTransport) Close() error                   { return nil }

var _ adapter.Transport = (*fakeTransport)(nil)

const testInterval = 5 * time.Millisecond

func testConfig(failureThreshold int) config.FailureDetectorConfig {
	return config.FailureDetectorConfig{
		HeartbeatInterval: testInterval,
		FailureThreshold:  failureThreshold,
	}
}

// newTestDetector wires a FailureDetector to a fakeTransport and a Reactor
// running on a background goroutine, per the reactor package's own
// single-goroutine contract.
func newTestDetector(t *testing.T, cfg config.FailureDetectorConfig) (*FailureDetector, *fakeTransport) {
	t.Helper()
	r := reactor.New(newFakeSource())
	ft := newFakeTransport()
	fd := NewFailureDetector(cfg, nil, ft, r)
	runReactor(t, r)
	return fd, ft
}

// nodeState, isHealthy, and healthyNodes read FailureDetector state via
// PostFunc rather than calling it directly: FailureDetector is reactor-owned
// state with no locking of its own (by design — see reactor's single-
// goroutine guarantee), so a direct call from the test goroutine would race
// with the reactor goroutine's own tick processing.
func nodeState(t *testing.T, fd *FailureDetector, id node.NodeID) node.NodeState {
	t.Helper()
	ch := make(chan node.NodeState, 1)
	fd.reactor.PostFunc(func() { ch <- fd.GetNodeState(id) })
	return <-ch
}

func isHealthy(t *testing.T, fd *FailureDetector, id node.NodeID) bool {
	t.Helper()
	ch := make(chan bool, 1)
	fd.reactor.PostFunc(func() { ch <- fd.IsNodeHealthy(id) })
	return <-ch
}

func healthyNodes(t *testing.T, fd *FailureDetector) []node.NodeID {
	t.Helper()
	ch := make(chan []node.NodeID, 1)
	fd.reactor.PostFunc(func() { ch <- fd.GetHealthyNodes() })
	return <-ch
}

// waitFor polls cond on the reactor goroutine (via PostFunc, since
// FailureDetector state must only be touched from there) until it is true
// or timeout elapses.
func waitFor(t *testing.T, r *reactor.Reactor, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		done := make(chan bool, 1)
		r.PostFunc(func() { done <- cond() })
		select {
		case ok := <-done:
			if ok {
				return
			}
		case <-time.After(timeout):
			t.Fatal("waitFor: reactor did not respond")
		}
		time.Sleep(testInterval)
	}
	t.Fatal("waitFor: condition never became true")
}

func TestFailureDetector_ConsecutiveMissesTransitionToFailedAndFireOnce(t *testing.T) {
	const threshold = 3
	fd, ft := newTestDetector(t, testConfig(threshold))
	peer := node.NodeID("peer-1")
	ft.enqueue(peer, errors.New("unreachable"), errors.New("unreachable"), errors.New("unreachable"))

	var failedCount int
	var mu sync.Mutex
	fd.reactor.PostFunc(func() {
		fd.OnNodeFailed = func(node.NodeID) {
			mu.Lock()
			failedCount++
			mu.Unlock()
		}
		fd.Start([]node.NodeID{peer})
	})

	waitFor(t, fd.reactor, time.Second, func() bool {
		return fd.GetNodeState(peer) == node.NodeStateFailed
	})

	// Let a few more ticks pass to confirm the fail action doesn't re-fire.
	ft.enqueue(peer, errors.New("unreachable"), errors.New("unreachable"))
	time.Sleep(10 * testInterval)

	mu.Lock()
	got := failedCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected OnNodeFailed to fire exactly once, got %d", got)
	}
}

func TestFailureDetector_RecoveryFiresOnceOnFailedToActive(t *testing.T) {
	const threshold = 2
	fd, ft := newTestDetector(t, testConfig(threshold))
	peer := node.NodeID("peer-1")
	ft.enqueue(peer, errors.New("unreachable"), errors.New("unreachable"))

	var recoveries int
	var mu sync.Mutex
	fd.reactor.PostFunc(func() {
		fd.OnNodeRecovery = func(node.NodeID) {
			mu.Lock()
			recoveries++
			mu.Unlock()
		}
		fd.Start([]node.NodeID{peer})
	})

	waitFor(t, fd.reactor, time.Second, func() bool {
		return fd.GetNodeState(peer) == node.NodeStateFailed
	})

	// Recovers, then keeps succeeding: recovery must fire exactly once.
	ft.enqueue(peer, nil, nil, nil, nil)
	waitFor(t, fd.reactor, time.Second, func() bool {
		return fd.GetNodeState(peer) == node.NodeStateActive
	})
	time.Sleep(10 * testInterval)

	mu.Lock()
	got := recoveries
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected OnNodeRecovery to fire exactly once, got %d", got)
	}
}

func TestFailureDetector_GetHealthyNodesAndIsNodeHealthy(t *testing.T) {
	fd, ft := newTestDetector(t, testConfig(2))
	healthy := node.NodeID("healthy")
	failing := node.NodeID("failing")
	ft.enqueue(failing, errors.New("unreachable"), errors.New("unreachable"))

	fd.reactor.PostFunc(func() {
		fd.Start([]node.NodeID{healthy, failing})
	})

	waitFor(t, fd.reactor, time.Second, func() bool {
		return fd.GetNodeState(healthy) == node.NodeStateActive &&
			fd.GetNodeState(failing) == node.NodeStateFailed
	})

	if !isHealthy(t, fd, healthy) {
		t.Error("expected healthy peer to report healthy")
	}
	if isHealthy(t, fd, failing) {
		t.Error("expected failing peer to report unhealthy")
	}

	got := healthyNodes(t, fd)
	if len(got) != 1 || got[0] != healthy {
		t.Fatalf("expected only %q in GetHealthyNodes, got %v", healthy, got)
	}
}

func TestFailureDetector_UpdateNodeStateForcesStateBypassingHeartbeats(t *testing.T) {
	fd, ft := newTestDetector(t, testConfig(5))
	peer := node.NodeID("peer-1")

	fd.reactor.PostFunc(func() {
		fd.Start([]node.NodeID{peer})
	})
	waitFor(t, fd.reactor, time.Second, func() bool {
		return fd.GetNodeState(peer) == node.NodeStateActive
	})

	done := make(chan struct{})
	fd.reactor.PostFunc(func() {
		fd.UpdateNodeState(peer, node.NodeStateLeaving)
		close(done)
	})
	<-done

	if got := nodeState(t, fd, peer); got != node.NodeStateLeaving {
		t.Fatalf("expected forced state Leaving, got %v", got)
	}

	// Subsequent successful heartbeats must not pull it back to Active.
	ft.enqueue(peer, nil, nil, nil)
	time.Sleep(10 * testInterval)
	if got := nodeState(t, fd, peer); got != node.NodeStateLeaving {
		t.Fatalf("expected state to remain Leaving after heartbeats, got %v", got)
	}
}

func TestFailureDetector_StopHaltsFurtherHeartbeats(t *testing.T) {
	fd, ft := newTestDetector(t, testConfig(5))
	peer := node.NodeID("peer-1")

	fd.reactor.PostFunc(func() {
		fd.Start([]node.NodeID{peer})
	})

	waitFor(t, fd.reactor, time.Second, func() bool {
		return ft.callCount(peer) >= 2
	})

	done := make(chan struct{})
	fd.reactor.PostFunc(func() {
		fd.Stop()
		close(done)
	})
	<-done

	countAtStop := ft.callCount(peer)
	time.Sleep(10 * testInterval)
	if got := ft.callCount(peer); got != countAtStop {
		t.Fatalf("expected no heartbeats after Stop, count went from %d to %d", countAtStop, got)
	}
}
