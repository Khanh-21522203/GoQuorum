package coordinator

import (
	"errors"
	"sync"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/reactor"
)

// --- reactor test scaffolding (mirrors engine/gossip's test harness) ---

// fakeSource is a minimal, controllable reactor.EventSource: Poll blocks
// until Wake is called or the deadline passes.
type fakeSource struct {
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
// test; only test code spans goroutines, product code stays single
// threaded.
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

// --- fake adapter.Storage (backs the local node) ---

type fakeStorage struct {
	rt     *reactor.Reactor
	nodeID node.NodeID

	mu       sync.Mutex
	putFail  bool
	putDelay time.Duration
	putCalls []*adapter.SiblingSet
	getResp  *adapter.SiblingSet
	getErr   error
	getDelay time.Duration
}

func newFakeStorage(rt *reactor.Reactor, id node.NodeID) *fakeStorage {
	return &fakeStorage{rt: rt, nodeID: id}
}

func (s *fakeStorage) setPutBehavior(fail bool, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putFail, s.putDelay = fail, delay
}

func (s *fakeStorage) setGetResponse(ss *adapter.SiblingSet, err error, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getResp, s.getErr, s.getDelay = ss, err, delay
}

func (s *fakeStorage) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.putCalls)
}

func (s *fakeStorage) Put(key []byte, siblings *adapter.SiblingSet, done func(error)) {
	s.mu.Lock()
	s.putCalls = append(s.putCalls, siblings)
	fail, delay := s.putFail, s.putDelay
	s.mu.Unlock()

	fire := func() {
		if fail {
			done(errors.New("fake storage: put failed"))
			return
		}
		done(nil)
	}
	if delay > 0 {
		s.rt.ScheduleOnce(delay, fire)
		return
	}
	fire()
}

func (s *fakeStorage) Get(key []byte, done func(*adapter.SiblingSet, error)) {
	s.mu.Lock()
	resp, err, delay := s.getResp, s.getErr, s.getDelay
	s.mu.Unlock()

	fire := func() { done(resp, err) }
	if delay > 0 {
		s.rt.ScheduleOnce(delay, fire)
		return
	}
	fire()
}

func (s *fakeStorage) GetRaw(key []byte, done func(*adapter.SiblingSet, error))      { s.Get(key, done) }
func (s *fakeStorage) Delete(key []byte, ctx vclock.VectorClock, done func(error))   { done(nil) }
func (s *fakeStorage) Scan(start, end []byte, fn adapter.ScanFunc, done func(error)) { done(nil) }
func (s *fakeStorage) LocalNodeID() node.NodeID                                      { return s.nodeID }
func (s *fakeStorage) Stats() adapter.StorageStats                                   { return adapter.StorageStats{} }
func (s *fakeStorage) Close() error                                                  { return nil }

// --- fake adapter.Transport (backs every non-local node) ---

type fakeTransport struct {
	rt *reactor.Reactor

	mu       sync.Mutex
	putFail  map[node.NodeID]bool
	putDelay map[node.NodeID]time.Duration
	putCalls map[node.NodeID][]*adapter.SiblingSet
	getResp  map[node.NodeID]*adapter.SiblingSet
	getErr   map[node.NodeID]error
	getDelay map[node.NodeID]time.Duration
}

func newFakeTransport(rt *reactor.Reactor) *fakeTransport {
	return &fakeTransport{
		rt:       rt,
		putFail:  make(map[node.NodeID]bool),
		putDelay: make(map[node.NodeID]time.Duration),
		putCalls: make(map[node.NodeID][]*adapter.SiblingSet),
		getResp:  make(map[node.NodeID]*adapter.SiblingSet),
		getErr:   make(map[node.NodeID]error),
		getDelay: make(map[node.NodeID]time.Duration),
	}
}

func (f *fakeTransport) setPutBehavior(id node.NodeID, fail bool, delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putFail[id] = fail
	f.putDelay[id] = delay
}

func (f *fakeTransport) setGetResponse(id node.NodeID, ss *adapter.SiblingSet, err error, delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getResp[id] = ss
	f.getErr[id] = err
	f.getDelay[id] = delay
}

func (f *fakeTransport) putCount(id node.NodeID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.putCalls[id])
}

func (f *fakeTransport) RemotePut(id node.NodeID, key []byte, siblings *adapter.SiblingSet, done func(error)) {
	f.mu.Lock()
	f.putCalls[id] = append(f.putCalls[id], siblings)
	fail, delay := f.putFail[id], f.putDelay[id]
	f.mu.Unlock()

	fire := func() {
		if fail {
			done(errors.New("fake transport: put failed"))
			return
		}
		done(nil)
	}
	if delay > 0 {
		f.rt.ScheduleOnce(delay, fire)
		return
	}
	fire()
}

func (f *fakeTransport) RemoteGet(id node.NodeID, key []byte, done func(*adapter.SiblingSet, error)) {
	f.mu.Lock()
	resp, err, delay := f.getResp[id], f.getErr[id], f.getDelay[id]
	f.mu.Unlock()

	fire := func() { done(resp, err) }
	if delay > 0 {
		f.rt.ScheduleOnce(delay, fire)
		return
	}
	fire()
}

func (f *fakeTransport) Heartbeat(id node.NodeID, done func(error))             { done(nil) }
func (f *fakeTransport) GetMerkleRoot(id node.NodeID, done func([]byte, error)) { done(nil, nil) }
func (f *fakeTransport) NotifyLeaving(id node.NodeID, done func(error))         { done(nil) }
func (f *fakeTransport) GossipExchange(id node.NodeID, entries []adapter.GossipEntry, done func([]adapter.GossipEntry, error)) {
	done(nil, nil)
}
func (f *fakeTransport) Dial(id node.NodeID, addr string) error { return nil }
func (f *fakeTransport) Close() error                           { return nil }

var _ adapter.Transport = (*fakeTransport)(nil)

// --- test setup ---

const (
	localID node.NodeID = "local"
	peer1ID node.NodeID = "peer-1"
	peer2ID node.NodeID = "peer-2"
)

func newRing(t *testing.T, ids ...node.NodeID) *hashring.HashRing {
	t.Helper()
	ring := hashring.NewHashRing(64)
	for _, id := range ids {
		if err := ring.AddNode(&node.Node{ID: id}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	return ring
}

// newTestCoordinator wires a Coordinator over fake storage/transport and a
// real hash ring holding localID, peer1ID, and peer2ID (so the default
// N=3/R=2/W=2 quorum config always spans all three). The reactor is
// returned unstarted so the caller can adjust timeouts before calling
// runInBackground.
func newTestCoordinator(t *testing.T) (*Coordinator, *reactor.Reactor, *fakeStorage, *fakeTransport) {
	t.Helper()
	ring := newRing(t, localID, peer1ID, peer2ID)
	rt := reactor.New(newFakeSource())
	st := newFakeStorage(rt, localID)
	tr := newFakeTransport(rt)
	c := NewCoordinator(localID, ring, st, tr, nil, rt, config.DefaultQuorumConfig())
	return c, rt, st, tr
}

// concurrentClock builds a vector clock with a single tick from tickers,
// so two clocks ticked by disjoint node IDs are concurrent (neither
// dominates the other).
func tickedClock(ids ...node.NodeID) vclock.VectorClock {
	vc := vclock.NewVectorClock()
	for _, id := range ids {
		vc.Tick(id)
	}
	return vc
}

// --- Put ---

func TestPut_SucceedsOnceWQuorumReached(t *testing.T) {
	c, rt, _, _ := newTestCoordinator(t)
	runInBackground(t, rt)

	done := make(chan struct {
		vc  vclock.VectorClock
		err error
	}, 1)
	c.Put("k1", []byte("v1"), vclock.NewVectorClock(), func(vc vclock.VectorClock, err error) {
		done <- struct {
			vc  vclock.VectorClock
			err error
		}{vc, err}
	})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Put failed: %v", res.err)
		}
		if res.vc.Get(localID) != 1 {
			t.Fatalf("expected local node's counter to be ticked once, got %d", res.vc.Get(localID))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Put never called done")
	}
}

func TestPut_FailsWhenTooManyReplicasFail(t *testing.T) {
	c, rt, _, tr := newTestCoordinator(t)
	// Preference list for N=3 covers all three nodes; fail both peers so at
	// most 1 (local) of 3 can ever succeed, below W=2.
	tr.setPutBehavior(peer1ID, true, 0)
	tr.setPutBehavior(peer2ID, true, 0)
	runInBackground(t, rt)

	done := make(chan error, 1)
	c.Put("k1", []byte("v1"), vclock.NewVectorClock(), func(_ vclock.VectorClock, err error) {
		done <- err
	})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected Put to fail when quorum is unreachable")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Put never called done")
	}
}

func TestPut_FiresAsSoonAsWReached_WithoutWaitingForSlowReplica(t *testing.T) {
	c, rt, st, tr := newTestCoordinator(t)
	slow := 1 * time.Second
	// Only slow down whichever nodes are not the local one; the local write
	// always resolves synchronously via fakeStorage.
	tr.setPutBehavior(peer1ID, false, 0)
	tr.setPutBehavior(peer2ID, false, slow)
	st.setPutBehavior(false, 0)
	runInBackground(t, rt)

	start := time.Now()
	done := make(chan time.Duration, 1)
	c.Put("k1", []byte("v1"), vclock.NewVectorClock(), func(_ vclock.VectorClock, err error) {
		done <- time.Since(start)
		if err != nil {
			t.Errorf("Put failed: %v", err)
		}
	})

	select {
	case elapsed := <-done:
		if elapsed >= slow {
			t.Fatalf("Put waited for the slow replica: took %v (slow replica delay %v)", elapsed, slow)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Put never called done")
	}
}

func TestPut_TimesOutWhenQuorumNeverReached(t *testing.T) {
	c, rt, st, tr := newTestCoordinator(t)
	c.timeoutConfig.ClientTimeout = 50 * time.Millisecond
	longDelay := 500 * time.Millisecond
	st.setPutBehavior(false, longDelay)
	tr.setPutBehavior(peer1ID, false, longDelay)
	tr.setPutBehavior(peer2ID, false, longDelay)
	runInBackground(t, rt)

	start := time.Now()
	done := make(chan error, 1)
	c.Put("k1", []byte("v1"), vclock.NewVectorClock(), func(_ vclock.VectorClock, err error) {
		done <- err
	})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
		if elapsed := time.Since(start); elapsed >= longDelay {
			t.Fatalf("Put did not time out early: took %v", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Put never called done")
	}
}

// --- Get ---

func TestGet_MergesConcurrentSiblings(t *testing.T) {
	c, rt, st, tr := newTestCoordinator(t)
	prefList, err := c.ring.GetPreferenceList("k1", c.quorumConfig.N)
	if err != nil {
		t.Fatalf("GetPreferenceList: %v", err)
	}

	siblingA := adapter.Sibling{Value: []byte("a"), VClock: tickedClock("x")}
	siblingB := adapter.Sibling{Value: []byte("b"), VClock: tickedClock("y")}
	setReplicaGet(c, st, tr, prefList[0], &adapter.SiblingSet{Siblings: []adapter.Sibling{siblingA}}, nil, 0)
	setReplicaGet(c, st, tr, prefList[1], &adapter.SiblingSet{Siblings: []adapter.Sibling{siblingB}}, nil, 0)
	// Third replica is never consulted before quorum resolves; give it
	// something harmless in case it is.
	setReplicaGet(c, st, tr, prefList[2], &adapter.SiblingSet{Siblings: []adapter.Sibling{siblingA}}, nil, 0)
	runInBackground(t, rt)

	done := make(chan struct {
		sibs []adapter.Sibling
		err  error
	}, 1)
	c.Get("k1", func(sibs []adapter.Sibling, err error) {
		done <- struct {
			sibs []adapter.Sibling
			err  error
		}{sibs, err}
	})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Get failed: %v", res.err)
		}
		if len(res.sibs) != 2 {
			t.Fatalf("expected 2 concurrent siblings, got %d: %+v", len(res.sibs), res.sibs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get never called done")
	}
}

func TestGet_DropsDominatedSibling(t *testing.T) {
	c, rt, st, tr := newTestCoordinator(t)
	prefList, err := c.ring.GetPreferenceList("k1", c.quorumConfig.N)
	if err != nil {
		t.Fatalf("GetPreferenceList: %v", err)
	}

	older := tickedClock(prefList[0])
	newer := older.Copy()
	newer.Tick(prefList[0])

	setReplicaGet(c, st, tr, prefList[0], &adapter.SiblingSet{Siblings: []adapter.Sibling{{Value: []byte("old"), VClock: older}}}, nil, 0)
	setReplicaGet(c, st, tr, prefList[1], &adapter.SiblingSet{Siblings: []adapter.Sibling{{Value: []byte("new"), VClock: newer}}}, nil, 0)
	setReplicaGet(c, st, tr, prefList[2], &adapter.SiblingSet{Siblings: []adapter.Sibling{{Value: []byte("new"), VClock: newer}}}, nil, 0)
	runInBackground(t, rt)

	done := make(chan []adapter.Sibling, 1)
	c.Get("k1", func(sibs []adapter.Sibling, err error) {
		if err != nil {
			t.Errorf("Get failed: %v", err)
		}
		done <- sibs
	})

	select {
	case sibs := <-done:
		if len(sibs) != 1 {
			t.Fatalf("expected the dominated sibling to be dropped, got %d siblings: %+v", len(sibs), sibs)
		}
		if string(sibs[0].Value) != "new" {
			t.Fatalf("expected the dominating sibling to survive, got %q", sibs[0].Value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get never called done")
	}
}

func TestGet_TriggersReadRepairOnStaleReplica(t *testing.T) {
	c, rt, st, tr := newTestCoordinator(t)
	prefList, err := c.ring.GetPreferenceList("k1", c.quorumConfig.N)
	if err != nil {
		t.Fatalf("GetPreferenceList: %v", err)
	}

	stale := prefList[0]
	fresh := prefList[1]

	staleClock := tickedClock(fresh)
	freshClock := staleClock.Copy()
	freshClock.Tick(fresh)

	setReplicaGet(c, st, tr, stale, &adapter.SiblingSet{Siblings: []adapter.Sibling{{Value: []byte("old"), VClock: staleClock}}}, nil, 0)
	setReplicaGet(c, st, tr, fresh, &adapter.SiblingSet{Siblings: []adapter.Sibling{{Value: []byte("new"), VClock: freshClock}}}, nil, 0)
	setReplicaGet(c, st, tr, prefList[2], &adapter.SiblingSet{Siblings: []adapter.Sibling{{Value: []byte("new"), VClock: freshClock}}}, nil, 0)
	runInBackground(t, rt)

	done := make(chan struct{}, 1)
	c.Get("k1", func(sibs []adapter.Sibling, err error) {
		if err != nil {
			t.Errorf("Get failed: %v", err)
		}
		close(done)
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Get never called done")
	}

	// Read repair (engine/readrepair.ReadRepairer) only holds an
	// adapter.Transport, never adapter.Storage, so it always repairs via
	// RemotePut even when the stale replica happens to be the local node.
	if tr.putCount(stale) == 0 {
		t.Fatalf("expected RemotePut to repair stale replica %s, got no calls", stale)
	}
	if tr.putCount(fresh) != 0 {
		t.Fatalf("did not expect a repair write for the already-fresh replica %s", fresh)
	}
}

func TestGet_TimesOutWhenQuorumNeverReached(t *testing.T) {
	c, rt, st, tr := newTestCoordinator(t)
	c.timeoutConfig.ClientTimeout = 50 * time.Millisecond
	longDelay := 500 * time.Millisecond
	st.setGetResponse(nil, errors.New("fake: unreachable"), longDelay)
	tr.setGetResponse(peer1ID, nil, errors.New("fake: unreachable"), longDelay)
	tr.setGetResponse(peer2ID, nil, errors.New("fake: unreachable"), longDelay)
	runInBackground(t, rt)

	start := time.Now()
	done := make(chan error, 1)
	c.Get("k1", func(_ []adapter.Sibling, err error) {
		done <- err
	})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
		if elapsed := time.Since(start); elapsed >= longDelay {
			t.Fatalf("Get did not time out early: took %v", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get never called done")
	}
}

// setReplicaGet configures node id's canned Get response, whether id is the
// coordinator's local node (fakeStorage) or a remote one (fakeTransport).
func setReplicaGet(c *Coordinator, st *fakeStorage, tr *fakeTransport, id node.NodeID, ss *adapter.SiblingSet, err error, delay time.Duration) {
	if id == localID {
		st.setGetResponse(ss, err, delay)
		return
	}
	_ = c
	tr.setGetResponse(id, ss, err, delay)
}
