package antientropy

import (
	"bytes"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

// ── fakeSource: EventSource good enough to drive a Reactor in tests ────────

// fakeSource never delivers real Events; it only exists so a Reactor has
// something to Poll while its timers and PostFunc-posted tasks do the
// actual work these tests observe.
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

// runReactor starts r.Run on a background goroutine and stops it, asserting
// a clean shutdown, once the test ends.
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

// callOnReactor runs fn on r's own goroutine and returns its result. Every
// AntiEntropy method call in these tests goes through this helper (rather
// than being called directly from the test goroutine) because AntiEntropy
// is reactor-owned state with no locking of its own, by design.
func callOnReactor[T any](t *testing.T, r *reactor.Reactor, fn func() T) T {
	t.Helper()
	ch := make(chan T, 1)
	r.PostFunc(func() { ch <- fn() })
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("PostFunc did not run within timeout")
		var zero T
		return zero
	}
}

// waitForCond polls cond, which must itself be safe to call from the test
// goroutine (i.e. it only touches state with its own locking, such as
// fakeTransport's counters), until it is true or timeout elapses.
func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("waitForCond: condition never became true")
}

// ── memStorage: controllable storage.Storage ───────────────────────────────

type memStorage struct {
	mu      sync.Mutex
	data    map[string]*storage.SiblingSet
	localID node.NodeID
	scanErr error
}

func newMemStorage(id node.NodeID) *memStorage {
	return &memStorage{data: make(map[string]*storage.SiblingSet), localID: id}
}

func (fs *memStorage) put(key []byte, ss *storage.SiblingSet) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.data[string(key)] = ss
}

func (fs *memStorage) Get(key []byte, done func(*storage.SiblingSet, error)) {
	fs.mu.Lock()
	ss := fs.data[string(key)]
	fs.mu.Unlock()
	done(ss, nil)
}

func (fs *memStorage) GetRaw(key []byte, done func(*storage.SiblingSet, error)) {
	fs.Get(key, done)
}

func (fs *memStorage) Put(key []byte, siblings *storage.SiblingSet, done func(error)) {
	fs.put(key, siblings)
	done(nil)
}

func (fs *memStorage) Delete(key []byte, _ vclock.VectorClock, done func(error)) {
	fs.mu.Lock()
	delete(fs.data, string(key))
	fs.mu.Unlock()
	done(nil)
}

// Scan visits keys in sorted order so tests get deterministic behavior, and
// fails outright with scanErr if the test configured one.
func (fs *memStorage) Scan(_, _ []byte, fn storage.ScanFunc, done func(error)) {
	fs.mu.Lock()
	if fs.scanErr != nil {
		err := fs.scanErr
		fs.mu.Unlock()
		done(err)
		return
	}
	keys := make([]string, 0, len(fs.data))
	for k := range fs.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type kv struct {
		key []byte
		ss  *storage.SiblingSet
	}
	items := make([]kv, 0, len(keys))
	for _, k := range keys {
		items = append(items, kv{key: []byte(k), ss: fs.data[k]})
	}
	fs.mu.Unlock()

	for _, item := range items {
		if !fn(item.key, item.ss) {
			break
		}
	}
	done(nil)
}

func (fs *memStorage) LocalNodeID() node.NodeID { return fs.localID }
func (fs *memStorage) Stats() storage.Stats     { return storage.Stats{} }
func (fs *memStorage) Close() error             { return nil }

var _ storage.Storage = (*memStorage)(nil)

// ── fakeTransport: controllable transport.Transport ─────────────────────────

type fakeTransport struct {
	mu        sync.Mutex
	roots     map[node.NodeID][]byte
	rootErr   map[node.NodeID]error
	rootCalls int
	putCalls  []putCall
	putErr    error // if set, every RemotePut fails with this error
}

type putCall struct {
	peer node.NodeID
	key  []byte
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		roots:   make(map[node.NodeID][]byte),
		rootErr: make(map[node.NodeID]error),
	}
}

func (ft *fakeTransport) setRoot(id node.NodeID, root []byte) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.roots[id] = root
}

func (ft *fakeTransport) setPutErr(err error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.putErr = err
}

func (ft *fakeTransport) putCallCount() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.putCalls)
}

func (ft *fakeTransport) getRootCallCount() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.rootCalls
}

func (ft *fakeTransport) RemotePut(id node.NodeID, key []byte, _ *storage.SiblingSet, done func(error)) {
	ft.mu.Lock()
	ft.putCalls = append(ft.putCalls, putCall{peer: id, key: append([]byte(nil), key...)})
	err := ft.putErr
	ft.mu.Unlock()
	done(err)
}

func (ft *fakeTransport) RemoteGet(node.NodeID, []byte, func(*storage.SiblingSet, error)) {
	panic("not used by these tests")
}

func (ft *fakeTransport) Heartbeat(node.NodeID, func(error)) {
	panic("not used by these tests")
}

func (ft *fakeTransport) GetMerkleRoot(id node.NodeID, done func([]byte, error)) {
	ft.mu.Lock()
	ft.rootCalls++
	root := ft.roots[id] // nil if the test never configured one
	err := ft.rootErr[id]
	ft.mu.Unlock()
	done(root, err)
}

func (ft *fakeTransport) NotifyLeaving(node.NodeID, func(error)) {
	panic("not used by these tests")
}

func (ft *fakeTransport) GossipExchange(node.NodeID, []transport.GossipEntry, func([]transport.GossipEntry, error)) {
	panic("not used by these tests")
}

func (ft *fakeTransport) Close() error { return nil }

var _ transport.Transport = (*fakeTransport)(nil)

// ── test wiring helpers ──────────────────────────────────────────────────

func newTestRing(t *testing.T, ids ...node.NodeID) *hashring.HashRing {
	t.Helper()
	ring := hashring.NewHashRing(4)
	for _, id := range ids {
		if err := ring.AddNode(&node.Node{ID: id, State: node.NodeStateActive}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	return ring
}

// newTestAntiEntropy wires an AntiEntropy to fake storage/transport and a
// Reactor running on a background goroutine, per the reactor package's own
// single-goroutine contract.
func newTestAntiEntropy(t *testing.T, cfg config.AntiEntropyConfig, ring *hashring.HashRing) (*AntiEntropy, *memStorage, *fakeTransport, *reactor.Reactor) {
	t.Helper()
	store := newMemStorage("n1")
	tr := newFakeTransport()
	ae := NewAntiEntropy("n1", store, ring, tr, cfg)
	r := reactor.New(newFakeSource())
	ae.SetReactor(r)
	runReactor(t, r)
	return ae, store, tr, r
}

// ── tests ────────────────────────────────────────────────────────────────

func TestAntiEntropy_Start_BuildsTreeAndArmsTimer(t *testing.T) {
	ring := newTestRing(t, "n1", "n2")
	cfg := config.AntiEntropyConfig{Enabled: true, ScanInterval: 5 * time.Millisecond, MerkleDepth: 4}
	ae, store, tr, r := newTestAntiEntropy(t, cfg, ring)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	store.put([]byte("k2"), sibling("n1", 1, []byte("v2")))

	if err := callOnReactor(t, r, func() error { return ae.Start() }); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := callOnReactor(t, r, ae.GetMerkleRoot)

	// An equivalent tree built directly over the same keys confirms Start
	// performed a real scan rather than leaving the tree empty.
	want := NewMerkleTree(cfg.MerkleDepth)
	want.UpdateKey([]byte("k1"), sibling("n1", 1, []byte("v1")))
	want.UpdateKey([]byte("k2"), sibling("n1", 1, []byte("v2")))
	if !bytes.Equal(got, want.GetRoot()) {
		t.Errorf("root after Start = %x, want %x", got, want.GetRoot())
	}

	// The scan timer being armed shows up as at least one round of
	// GetMerkleRoot calls against the peer within a generous window.
	waitForCond(t, time.Second, func() bool { return tr.getRootCallCount() > 0 })
}

func TestAntiEntropy_Start_Disabled_NoOp(t *testing.T) {
	ring := newTestRing(t, "n1")
	cfg := config.AntiEntropyConfig{Enabled: false, MerkleDepth: 4}
	// Deliberately never call SetReactor: Enabled=false must short-circuit
	// before the reactor is ever consulted.
	ae := NewAntiEntropy("n1", newMemStorage("n1"), ring, newFakeTransport(), cfg)

	if err := ae.Start(); err != nil {
		t.Fatalf("Start on a disabled AntiEntropy returned %v, want nil", err)
	}
}

func TestAntiEntropy_Start_TwiceIsSafeNoOp(t *testing.T) {
	ring := newTestRing(t, "n1")
	cfg := config.AntiEntropyConfig{Enabled: true, ScanInterval: time.Hour, MerkleDepth: 4}
	ae, store, _, r := newTestAntiEntropy(t, cfg, ring)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	if err := callOnReactor(t, r, func() error { return ae.Start() }); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	root1 := callOnReactor(t, r, ae.GetMerkleRoot)

	// Mutate the store after the first Start. If a second Start rebuilt
	// the tree, this key's contribution would show up in the new root.
	store.put([]byte("k2"), sibling("n1", 1, []byte("v2")))

	if err := callOnReactor(t, r, func() error { return ae.Start() }); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	root2 := callOnReactor(t, r, ae.GetMerkleRoot)

	if !bytes.Equal(root1, root2) {
		t.Errorf("second Start rebuilt the tree: root changed from %x to %x", root1, root2)
	}
}

func TestAntiEntropy_Stop_HaltsScanRounds(t *testing.T) {
	ring := newTestRing(t, "n1", "n2")
	cfg := config.AntiEntropyConfig{Enabled: true, ScanInterval: 5 * time.Millisecond, MerkleDepth: 4}
	ae, _, tr, r := newTestAntiEntropy(t, cfg, ring)

	if err := callOnReactor(t, r, func() error { return ae.Start() }); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForCond(t, time.Second, func() bool { return tr.getRootCallCount() > 0 })

	callOnReactor(t, r, func() int { ae.Stop(); return 0 })
	countAfterStop := tr.getRootCallCount()

	time.Sleep(50 * time.Millisecond) // several scan intervals' worth
	if got := tr.getRootCallCount(); got != countAfterStop {
		t.Errorf("scan rounds continued after Stop: call count went from %d to %d", countAfterStop, got)
	}
}

func TestAntiEntropy_TriggerWithPeer_MatchingRoot_NoPush(t *testing.T) {
	ring := newTestRing(t, "n1", "n2")
	cfg := config.AntiEntropyConfig{Enabled: true, ScanInterval: time.Hour, MerkleDepth: 4}
	ae, store, tr, r := newTestAntiEntropy(t, cfg, ring)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	if err := callOnReactor(t, r, func() error { return ae.Start() }); err != nil {
		t.Fatalf("Start: %v", err)
	}

	root := callOnReactor(t, r, ae.GetMerkleRoot)
	tr.setRoot("n2", root)

	callOnReactor(t, r, func() int { ae.TriggerWithPeer("n2"); return 0 })

	if got := tr.putCallCount(); got != 0 {
		t.Errorf("expected 0 RemotePut calls when roots match, got %d", got)
	}
}

func TestAntiEntropy_TriggerWithPeer_DivergingRoot_PushesKeys(t *testing.T) {
	ring := newTestRing(t, "n1", "n2")
	cfg := config.AntiEntropyConfig{Enabled: true, ScanInterval: time.Hour, MerkleDepth: 4}
	ae, store, tr, r := newTestAntiEntropy(t, cfg, ring)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	if err := callOnReactor(t, r, func() error { return ae.Start() }); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A root the peer would never actually produce for this keyspace.
	tr.setRoot("n2", bytes.Repeat([]byte{0xFF}, hashSize))

	callOnReactor(t, r, func() int { ae.TriggerWithPeer("n2"); return 0 })

	if got := tr.putCallCount(); got == 0 {
		t.Error("expected at least one RemotePut call when roots diverge, got 0")
	}
}

func TestAntiEntropy_SyncWithPeers_PushesToEveryPeerAndSucceeds(t *testing.T) {
	ring := newTestRing(t, "n1")
	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	ae, store, tr, r := newTestAntiEntropy(t, cfg, ring)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	store.put([]byte("k2"), sibling("n1", 1, []byte("v2")))

	doneCh := make(chan error, 1)
	r.PostFunc(func() {
		ae.SyncWithPeers([]node.NodeID{"n2", "n3"}, func(err error) { doneCh <- err })
	})

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("SyncWithPeers: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SyncWithPeers never called done")
	}

	// 2 keys pushed to each of 2 peers.
	if got := tr.putCallCount(); got != 4 {
		t.Errorf("expected 4 RemotePut calls, got %d", got)
	}
}

func TestAntiEntropy_SyncWithPeers_ReportsFailure(t *testing.T) {
	ring := newTestRing(t, "n1")
	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	ae, store, tr, r := newTestAntiEntropy(t, cfg, ring)

	store.put([]byte("k1"), sibling("n1", 1, []byte("v1")))
	pushErr := errors.New("remote put failed")
	tr.setPutErr(pushErr)

	doneCh := make(chan error, 1)
	r.PostFunc(func() {
		ae.SyncWithPeers([]node.NodeID{"n2"}, func(err error) { doneCh <- err })
	})

	select {
	case err := <-doneCh:
		if !errors.Is(err, pushErr) {
			t.Fatalf("SyncWithPeers done err = %v, want %v", err, pushErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SyncWithPeers never called done")
	}
}

func TestAntiEntropy_OnKeyUpdateAndDelete_ChangeRoot(t *testing.T) {
	ring := newTestRing(t, "n1")
	cfg := config.AntiEntropyConfig{Enabled: true, MerkleDepth: 4}
	ae, _, _, r := newTestAntiEntropy(t, cfg, ring)

	rootEmpty := callOnReactor(t, r, ae.GetMerkleRoot)

	ss := sibling("n1", 1, []byte("v1"))
	callOnReactor(t, r, func() int { ae.OnKeyUpdate([]byte("k1"), ss); return 0 })
	rootAfterUpdate := callOnReactor(t, r, ae.GetMerkleRoot)
	if bytes.Equal(rootEmpty, rootAfterUpdate) {
		t.Error("OnKeyUpdate did not change the root")
	}

	callOnReactor(t, r, func() int { ae.OnKeyDelete([]byte("k1"), ss); return 0 })
	rootAfterDelete := callOnReactor(t, r, ae.GetMerkleRoot)
	if !bytes.Equal(rootEmpty, rootAfterDelete) {
		t.Error("OnKeyDelete did not restore the root (XOR symmetry broken)")
	}
}
