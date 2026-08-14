package handoff

import (
	"errors"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

// fakeSource is a minimal EventSource: Poll blocks until Wake is called or
// the deadline passes, and never produces events. It is enough to drive a
// Reactor whose only work arrives via PostFunc and scheduled timers.
type fakeSource struct {
	wake chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{wake: make(chan struct{}, 1)}
}

func (f *fakeSource) Poll(dst []reactor.Event, deadline time.Time) ([]reactor.Event, error) {
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

// runInBackground starts r.Run on a background goroutine and arranges for
// it to be stopped and joined at test cleanup.
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

// runSync posts fn onto r's goroutine and blocks until it has run, giving
// tests a way to touch reactor-owned state (hh, mm) without racing the
// reactor goroutine.
func runSync(r *reactor.Reactor, fn func()) {
	done := make(chan struct{})
	r.PostFunc(func() {
		fn()
		close(done)
	})
	<-done
}

// remotePutCall records one RemotePut invocation observed by fakeTransport.
type remotePutCall struct {
	id  node.NodeID
	key []byte
}

// fakeTransport is a transport.Transport whose RemotePut behavior is
// controlled by the test via err: every RemotePut call is recorded and its
// done callback is invoked synchronously with the configured err, matching
// the real done-invoked-from-the-issuing-goroutine contract.
type fakeTransport struct {
	calls []remotePutCall
	err   error
}

func (f *fakeTransport) RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error)) {
	f.calls = append(f.calls, remotePutCall{id: id, key: append([]byte(nil), key...)})
	done(f.err)
}

func (f *fakeTransport) RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, nil)
}

func (f *fakeTransport) Heartbeat(id node.NodeID, done func(error)) { done(nil) }

func (f *fakeTransport) GetMerkleRoot(id node.NodeID, done func([]byte, error)) { done(nil, nil) }

func (f *fakeTransport) NotifyLeaving(id node.NodeID, done func(error)) { done(nil) }

func (f *fakeTransport) GossipExchange(id node.NodeID, entries []transport.GossipEntry, done func([]transport.GossipEntry, error)) {
	done(nil, nil)
}

func (f *fakeTransport) Close() error { return nil }

func newTestManager() *membership.MembershipManager {
	return membership.NewMembershipManager(membership.Config{NodeID: "local"}, "test")
}

func newTestHandoff(t *testing.T, mm *membership.MembershipManager, ft *fakeTransport) (*HintedHandoff, *reactor.Reactor) {
	t.Helper()
	r := reactor.New(newFakeSource())
	runInBackground(t, r)
	hh := NewHintedHandoff(mm, ft, "local", r)
	return hh, r
}

func TestStoreHint_HintCountReflectsStoredHints(t *testing.T) {
	hh, _ := newTestHandoff(t, newTestManager(), &fakeTransport{})

	if got := hh.HintCount("target"); got != 0 {
		t.Fatalf("HintCount() = %d before any StoreHint, want 0", got)
	}

	if err := hh.StoreHint("target", []byte("k1"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}
	if err := hh.StoreHint("target", []byte("k2"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}

	if got := hh.HintCount("target"); got != 2 {
		t.Fatalf("HintCount() = %d, want 2", got)
	}
}

func TestStoreHint_EvictsOldestOnceAtCapacity(t *testing.T) {
	hh, _ := newTestHandoff(t, newTestManager(), &fakeTransport{})

	for i := 0; i < maxHintsPerNode; i++ {
		key := []byte{byte(i), byte(i >> 8)}
		if err := hh.StoreHint("target", key, &storage.SiblingSet{}); err != nil {
			t.Fatalf("StoreHint() error = %v", err)
		}
	}
	if got := hh.HintCount("target"); got != maxHintsPerNode {
		t.Fatalf("HintCount() = %d, want %d", got, maxHintsPerNode)
	}

	// One more hint should evict the oldest (key for i=0) rather than grow
	// past capacity.
	if err := hh.StoreHint("target", []byte("overflow"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}
	if got := hh.HintCount("target"); got != maxHintsPerNode {
		t.Fatalf("HintCount() = %d after overflow, want %d", got, maxHintsPerNode)
	}

	list := hh.hints["target"]
	oldestKey := list[0].Key
	if len(oldestKey) != 2 || oldestKey[0] != 1 || oldestKey[1] != 0 {
		t.Fatalf("oldest remaining hint key = %v, want the key for i=1 (i=0 should have been evicted)", oldestKey)
	}
	newestKey := list[len(list)-1].Key
	if string(newestKey) != "overflow" {
		t.Fatalf("newest hint key = %q, want %q", newestKey, "overflow")
	}
}

func TestReplay_DeliversToActiveTargetAndClearsOnSuccess(t *testing.T) {
	mm := newTestManager()
	ft := &fakeTransport{}
	hh, r := newTestHandoff(t, mm, ft)

	runSync(r, func() {
		mm.UpdatePeerStatus("target", membership.NodeStatusActive)
		hh.Start()
	})
	if err := hh.StoreHint("target", []byte("k1"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}

	runSync(r, hh.replay)

	if len(ft.calls) != 1 {
		t.Fatalf("RemotePut called %d times, want 1", len(ft.calls))
	}
	if ft.calls[0].id != "target" {
		t.Fatalf("RemotePut target = %v, want %v", ft.calls[0].id, "target")
	}

	var count int
	runSync(r, func() { count = hh.HintCount("target") })
	if count != 0 {
		t.Fatalf("HintCount() = %d after successful replay, want 0", count)
	}
}

func TestReplay_FailedDeliveryKeepsHintForNextTick(t *testing.T) {
	mm := newTestManager()
	ft := &fakeTransport{err: errors.New("unreachable")}
	hh, r := newTestHandoff(t, mm, ft)

	runSync(r, func() {
		mm.UpdatePeerStatus("target", membership.NodeStatusActive)
		hh.Start()
	})
	if err := hh.StoreHint("target", []byte("k1"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}

	runSync(r, hh.replay)

	var count int
	runSync(r, func() { count = hh.HintCount("target") })
	if count != 1 {
		t.Fatalf("HintCount() = %d after failed replay, want 1 (hint should be requeued)", count)
	}
	if len(ft.calls) != 1 {
		t.Fatalf("RemotePut called %d times in one tick, want 1 (no immediate retry)", len(ft.calls))
	}

	// Next tick, with the target now reachable, should deliver it.
	runSync(r, func() { ft.err = nil })
	runSync(r, hh.replay)

	runSync(r, func() { count = hh.HintCount("target") })
	if count != 0 {
		t.Fatalf("HintCount() = %d after retry succeeds, want 0", count)
	}
	if len(ft.calls) != 2 {
		t.Fatalf("RemotePut called %d times total, want 2", len(ft.calls))
	}
}

func TestReplay_SkipsInactiveTarget(t *testing.T) {
	mm := newTestManager()
	ft := &fakeTransport{}
	hh, r := newTestHandoff(t, mm, ft)

	// "target" is never marked active.
	runSync(r, func() { hh.Start() })
	if err := hh.StoreHint("target", []byte("k1"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}

	runSync(r, hh.replay)

	if len(ft.calls) != 0 {
		t.Fatalf("RemotePut called %d times for an inactive target, want 0", len(ft.calls))
	}
	var count int
	runSync(r, func() { count = hh.HintCount("target") })
	if count != 1 {
		t.Fatalf("HintCount() = %d, want 1 (hint untouched while target inactive)", count)
	}
}

func TestReplay_DropsHintOlderThanMaxAge(t *testing.T) {
	mm := newTestManager()
	ft := &fakeTransport{}
	hh, r := newTestHandoff(t, mm, ft)

	runSync(r, func() {
		mm.UpdatePeerStatus("target", membership.NodeStatusActive)
		hh.Start()
	})
	if err := hh.StoreHint("target", []byte("k1"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}
	// Backdate the hint past maxHintAge.
	runSync(r, func() { hh.hints["target"][0].CreatedAt = time.Now().Add(-maxHintAge - time.Minute) })

	runSync(r, hh.replay)

	if len(ft.calls) != 0 {
		t.Fatalf("RemotePut called %d times for an expired hint, want 0", len(ft.calls))
	}
	var count int
	runSync(r, func() { count = hh.HintCount("target") })
	if count != 0 {
		t.Fatalf("HintCount() = %d after expiry, want 0 (expired hint should be dropped)", count)
	}
}

func TestStop_HaltsFurtherReplayAttempts(t *testing.T) {
	mm := newTestManager()
	ft := &fakeTransport{}
	hh, r := newTestHandoff(t, mm, ft)

	runSync(r, func() {
		mm.UpdatePeerStatus("target", membership.NodeStatusActive)
		hh.Start()
	})
	if err := hh.StoreHint("target", []byte("k1"), &storage.SiblingSet{}); err != nil {
		t.Fatalf("StoreHint() error = %v", err)
	}
	runSync(r, hh.Stop)

	// A tick delivered after Stop (e.g. one already in flight when
	// CancelTimer ran) must not replay anything.
	runSync(r, hh.replay)

	if len(ft.calls) != 0 {
		t.Fatalf("RemotePut called %d times after Stop, want 0", len(ft.calls))
	}
	var count int
	runSync(r, func() { count = hh.HintCount("target") })
	if count != 1 {
		t.Fatalf("HintCount() = %d after Stop, want 1 (hint left untouched)", count)
	}
}
