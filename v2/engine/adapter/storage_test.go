package adapter

import (
	"errors"
	"testing"
	"time"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/storage/journal"
)

func newTestStorageAdapter(t *testing.T) (*StorageAdapter, *journal.Store, *ioruntime.Runtime) {
	t.Helper()
	rt, err := ioruntime.New(64)
	if err != nil {
		t.Fatalf("ioruntime.New: %v", err)
	}
	dir := t.TempDir()
	store, err := journal.Open(rt, journal.Options{DataDir: dir, NumSegments: 2, SegmentSize: 64 * 1024})
	if err != nil {
		t.Fatalf("journal.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = rt.Close()
	})
	adapter := NewStorageAdapter(store, "node-1")
	return adapter, store, rt
}

func drainStorageCompletions(rt *ioruntime.Runtime, store *journal.Store) {
	var evs [16]reactor.Event
	for i := 0; i < 5; i++ {
		n, _ := rt.Poll(evs[:0], time.Now().Add(10*time.Millisecond))
		if len(n) == 0 {
			break
		}
		for _, ev := range n {
			store.HandleCompletion(ev)
		}
	}
}

func TestStorageAdapter_PutGetDeleteRoundTrip(t *testing.T) {
	adapter, store, rt := newTestStorageAdapter(t)

	if adapter.LocalNodeID() != "node-1" {
		t.Fatalf("LocalNodeID = %q, want node-1", adapter.LocalNodeID())
	}

	key := []byte("user:1")
	vc := vclock.NewVectorClock()
	vc.Set("node-1", 1)
	ss := &SiblingSet{
		Siblings: []Sibling{{
			Value:     []byte("Alice"),
			VClock:    vc,
			Timestamp: time.Now().Unix(),
		}},
	}

	var putErr error
	adapter.Put(key, ss, func(err error) { putErr = err })
	drainStorageCompletions(rt, store)

	if putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	var got *SiblingSet
	var getErr error
	adapter.Get(key, func(res *SiblingSet, err error) {
		got = res
		getErr = err
	})
	drainStorageCompletions(rt, store)

	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if len(got.Siblings) != 1 || string(got.Siblings[0].Value) != "Alice" {
		t.Fatalf("unexpected Get result: %+v", got)
	}

	// Delete
	var delErr error
	adapter.Delete(key, vc, func(err error) { delErr = err })
	drainStorageCompletions(rt, store)

	if delErr != nil {
		t.Fatalf("Delete: %v", delErr)
	}

	// Get should return ErrKeyNotFound (tombstone filtered out)
	adapter.Get(key, func(res *SiblingSet, err error) {
		got = res
		getErr = err
	})
	drainStorageCompletions(rt, store)

	if !errors.Is(getErr, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after Delete, got %v", getErr)
	}

	// GetRaw should return the tombstone sibling
	var raw *SiblingSet
	var rawErr error
	adapter.GetRaw(key, func(res *SiblingSet, err error) {
		raw = res
		rawErr = err
	})
	drainStorageCompletions(rt, store)

	if rawErr != nil {
		t.Fatalf("GetRaw: %v", rawErr)
	}
	if len(raw.Siblings) < 1 || !raw.Siblings[len(raw.Siblings)-1].Tombstone {
		t.Fatalf("expected tombstone in GetRaw, got %+v", raw)
	}
}

func TestStorageAdapter_Scan(t *testing.T) {
	adapter, store, rt := newTestStorageAdapter(t)

	for _, k := range []string{"c", "a", "b"} {
		ss := &SiblingSet{Siblings: []Sibling{{Value: []byte("val-" + k)}}}
		adapter.Put([]byte(k), ss, func(err error) {})
		drainStorageCompletions(rt, store)
	}

	var scanned []string
	adapter.Scan(nil, nil, func(key []byte, ss *SiblingSet) bool {
		scanned = append(scanned, string(key))
		return true
	}, func(err error) {})
	drainStorageCompletions(rt, store)

	if len(scanned) != 3 || scanned[0] != "a" || scanned[1] != "b" || scanned[2] != "c" {
		t.Fatalf("unexpected scan results: %v", scanned)
	}
}

func TestStorageAdapter_Compact(t *testing.T) {
	adapter, store, rt := newTestStorageAdapter(t)

	// 1. Write live key
	ssLive := &SiblingSet{Siblings: []Sibling{{Value: []byte("alive-val")}}}
	adapter.Put([]byte("live-key"), ssLive, func(err error) {})
	drainStorageCompletions(rt, store)

	// 2. Write key and delete it (tombstone)
	ssDead := &SiblingSet{Siblings: []Sibling{{Value: []byte("dead-val")}}}
	adapter.Put([]byte("dead-key"), ssDead, func(err error) {})
	drainStorageCompletions(rt, store)

	adapter.Delete([]byte("dead-key"), vclock.VectorClock{}, func(err error) {})
	drainStorageCompletions(rt, store)

	// Run compaction
	var compactStats journal.CompactStats
	var compactErr error
	adapter.Compact(func(stats journal.CompactStats, err error) {
		compactStats = stats
		compactErr = err
	})
	drainStorageCompletions(rt, store)

	if compactErr != nil {
		t.Fatalf("Compact failed: %v", compactErr)
	}
	if compactStats.LiveKeyCount != 1 {
		t.Fatalf("expected 1 live key after compaction, got %d", compactStats.LiveKeyCount)
	}

	// Live key should still exist
	var gotLive *SiblingSet
	adapter.Get([]byte("live-key"), func(res *SiblingSet, err error) {
		gotLive = res
	})
	drainStorageCompletions(rt, store)

	if gotLive == nil || string(gotLive.Siblings[0].Value) != "alive-val" {
		t.Fatalf("expected live-key to survive compaction, got %+v", gotLive)
	}

	// Dead key should be completely purged from KV store
	var gotDead *SiblingSet
	var deadErr error
	adapter.GetRaw([]byte("dead-key"), func(res *SiblingSet, err error) {
		gotDead = res
		deadErr = err
	})
	drainStorageCompletions(rt, store)

	if !errors.Is(deadErr, quorumerr.ErrKeyNotFound) || gotDead != nil {
		t.Fatalf("expected dead-key to be purged during compaction, got %+v, err=%v", gotDead, deadErr)
	}
}

func TestStorageAdapter_DirectHandler(t *testing.T) {
	adapter, _, _ := newTestStorageAdapter(t)

	var errorReported error
	adapter.OnStorageErrorHook = func(err error) { errorReported = err }
	adapter.OnStorageError(errors.New("disk failure"))

	if errorReported == nil || errorReported.Error() != "disk failure" {
		t.Fatalf("expected disk failure, got %v", errorReported)
	}

	// Test direct OnReadComplete with not found
	adapter.Get([]byte("missing"), func(ss *SiblingSet, err error) {
		if !errors.Is(err, quorumerr.ErrKeyNotFound) {
			t.Fatalf("expected ErrKeyNotFound, got %v", err)
		}
	})
	adapter.OnReadComplete(adapter.nextReqID, []byte("missing"), nil, quorumerr.ErrKeyNotFound)
}
