package handoff

import (
	"errors"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
)

type remotePutCall struct {
	id  node.NodeID
	key []byte
}

type fakeTransport struct {
	calls []remotePutCall
	err   error
}

func (f *fakeTransport) RemotePut(id node.NodeID, corrID uint64, key []byte, siblings *adapter.SiblingSet) error {
	f.calls = append(f.calls, remotePutCall{id: id, key: append([]byte(nil), key...)})
	return f.err
}

func (f *fakeTransport) RemoteGet(id node.NodeID, corrID uint64, key []byte) error {
	return nil
}
func (f *fakeTransport) Heartbeat(id node.NodeID, corrID uint64) error     { return nil }
func (f *fakeTransport) GetMerkleRoot(id node.NodeID, corrID uint64) error { return nil }
func (f *fakeTransport) NotifyLeaving(id node.NodeID, corrID uint64) error { return nil }
func (f *fakeTransport) GossipExchange(id node.NodeID, corrID uint64, entries []adapter.GossipEntry) error {
	return nil
}
func (f *fakeTransport) Dial(id node.NodeID, addr string) error { return nil }
func (f *fakeTransport) Close() error                           { return nil }

func TestStoreHint_HintCount(t *testing.T) {
	hh := NewHintedHandoff(&fakeTransport{}, "local")

	if got := hh.HintCount("target"); got != 0 {
		t.Fatalf("HintCount() = %d before any StoreHint, want 0", got)
	}

	_ = hh.StoreHint("target", []byte("k1"), &adapter.SiblingSet{})
	_ = hh.StoreHint("target", []byte("k2"), &adapter.SiblingSet{})

	if got := hh.HintCount("target"); got != 2 {
		t.Fatalf("HintCount() = %d, want 2", got)
	}
}

func TestStoreHint_EvictsOldestAtCapacity(t *testing.T) {
	hh := NewHintedHandoff(&fakeTransport{}, "local")

	for i := 0; i < maxHintsPerNode; i++ {
		key := []byte{byte(i), byte(i >> 8)}
		_ = hh.StoreHint("target", key, &adapter.SiblingSet{})
	}

	_ = hh.StoreHint("target", []byte("overflow"), &adapter.SiblingSet{})

	if got := hh.HintCount("target"); got != maxHintsPerNode {
		t.Fatalf("HintCount() = %d, want %d", got, maxHintsPerNode)
	}

	list := hh.hints["target"]
	oldestKey := list[0].Key
	if len(oldestKey) != 2 || oldestKey[0] != 1 || oldestKey[1] != 0 {
		t.Fatalf("oldest remaining hint key = %v, want i=1", oldestKey)
	}
}

func TestReplay_DeliversToActiveTarget(t *testing.T) {
	ft := &fakeTransport{}
	hh := NewHintedHandoff(ft, "local")

	_ = hh.StoreHint("target", []byte("k1"), &adapter.SiblingSet{})

	hh.Replay([]node.NodeID{"target"})

	if len(ft.calls) != 1 || ft.calls[0].id != "target" {
		t.Fatalf("expected RemotePut called for target, got %v", ft.calls)
	}
	if got := hh.HintCount("target"); got != 0 {
		t.Fatalf("expected HintCount = 0 after success, got %d", got)
	}
}

func TestReplay_FailedDeliveryRequeuesHint(t *testing.T) {
	ft := &fakeTransport{err: errors.New("unreachable")}
	hh := NewHintedHandoff(ft, "local")

	_ = hh.StoreHint("target", []byte("k1"), &adapter.SiblingSet{})

	hh.Replay([]node.NodeID{"target"})

	if got := hh.HintCount("target"); got != 1 {
		t.Fatalf("expected HintCount = 1 after failed replay, got %d", got)
	}

	// Retry with success
	ft.err = nil
	hh.Replay([]node.NodeID{"target"})

	if got := hh.HintCount("target"); got != 0 {
		t.Fatalf("expected HintCount = 0 after retry success, got %d", got)
	}
}

func TestReplay_SkipsInactiveTarget(t *testing.T) {
	ft := &fakeTransport{}
	hh := NewHintedHandoff(ft, "local")

	_ = hh.StoreHint("target", []byte("k1"), &adapter.SiblingSet{})

	hh.Replay([]node.NodeID{"other"})

	if len(ft.calls) != 0 {
		t.Fatalf("expected 0 calls for inactive target, got %d", len(ft.calls))
	}
	if got := hh.HintCount("target"); got != 1 {
		t.Fatalf("expected HintCount = 1, got %d", got)
	}
}

func TestReplay_DropsExpiredHints(t *testing.T) {
	ft := &fakeTransport{}
	hh := NewHintedHandoff(ft, "local")

	_ = hh.StoreHint("target", []byte("k1"), &adapter.SiblingSet{})
	hh.hints["target"][0].CreatedAt = time.Now().Add(-maxHintAge - time.Minute)

	hh.Replay([]node.NodeID{"target"})

	if len(ft.calls) != 0 {
		t.Fatalf("expected 0 calls for expired hint, got %d", len(ft.calls))
	}
	if got := hh.HintCount("target"); got != 0 {
		t.Fatalf("expected HintCount = 0 after drop, got %d", got)
	}
}
