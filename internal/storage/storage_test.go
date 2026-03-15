package storage

import (
	"testing"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/vclock"
)

func testStorage(t *testing.T) *Storage {
	t.Helper()
	opts := DefaultStorageOptions(t.TempDir(), "node1")
	opts.SyncWrites = false         // faster for tests
	opts.TombstoneGCEnabled = false // disable background GC; tests call runTombstoneGC directly
	opts.BlockCacheSize = 8 << 20   // 8MB
	opts.MemTableSize = 8 << 20     // 8MB
	s, err := NewStorage(opts)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeVC(node string, counter uint64) vclock.VectorClock {
	vc := vclock.NewVectorClock()
	vc.SetString(node, counter)
	return vc
}

// ── Put / Get round-trip ─────────────────────────────────────────────────────

func TestPutGetRoundTrip(t *testing.T) {
	s := testStorage(t)
	key := []byte("hello")
	value := []byte("world")

	vc := makeVC("node1", 1)
	sib := Sibling{Value: value, VClock: vc, Timestamp: time.Now().Unix()}
	if err := s.Put(key, &SiblingSet{Siblings: []Sibling{sib}}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Siblings) != 1 {
		t.Fatalf("expected 1 sibling, got %d", len(got.Siblings))
	}
	if string(got.Siblings[0].Value) != "world" {
		t.Errorf("wrong value: %s", got.Siblings[0].Value)
	}
}

// ── Get on missing key returns ErrKeyNotFound ────────────────────────────────

func TestGetMissingKey(t *testing.T) {
	s := testStorage(t)
	_, err := s.Get([]byte("nonexistent"))
	if err != common.ErrKeyNotFound {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

// ── Two concurrent Puts produce two siblings ─────────────────────────────────

func TestConcurrentWritesProduceSiblings(t *testing.T) {
	s := testStorage(t)
	key := []byte("key")

	// First write from node1
	vc1 := makeVC("node1", 1)
	s.Put(key, &SiblingSet{Siblings: []Sibling{
		{Value: []byte("v1"), VClock: vc1, Timestamp: 1},
	}})

	// Concurrent write from node2 (doesn't happen-after node1)
	vc2 := makeVC("node2", 1)
	s.Put(key, &SiblingSet{Siblings: []Sibling{
		{Value: []byte("v2"), VClock: vc2, Timestamp: 2},
	}})

	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Siblings) != 2 {
		t.Fatalf("expected 2 concurrent siblings, got %d", len(got.Siblings))
	}
}

// ── A causally dominating Put replaces the earlier sibling ──────────────────

func TestCausalPutReplacesPrior(t *testing.T) {
	s := testStorage(t)
	key := []byte("key")

	// Initial write
	vc1 := makeVC("node1", 1)
	s.Put(key, &SiblingSet{Siblings: []Sibling{
		{Value: []byte("old"), VClock: vc1, Timestamp: 1},
	}})

	// Causally subsequent write (vc2 happens-after vc1)
	vc2 := vc1.Copy()
	vc2.SetString("node1", 2)
	s.Put(key, &SiblingSet{Siblings: []Sibling{
		{Value: []byte("new"), VClock: vc2, Timestamp: 2},
	}})

	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Siblings) != 1 {
		t.Fatalf("expected 1 sibling after causal update, got %d", len(got.Siblings))
	}
	if string(got.Siblings[0].Value) != "new" {
		t.Errorf("wrong value: %s", got.Siblings[0].Value)
	}
}

// ── Delete followed by Get returns ErrKeyNotFound ───────────────────────────

func TestDeleteReturnsNotFound(t *testing.T) {
	s := testStorage(t)
	key := []byte("key")

	vc := makeVC("node1", 1)
	s.Put(key, &SiblingSet{Siblings: []Sibling{
		{Value: []byte("val"), VClock: vc, Timestamp: 1},
	}})

	// Delete with a causally subsequent VC
	vcDel := vc.Copy()
	vcDel.SetString("node1", 2)
	if err := s.Delete(key, vcDel); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get(key)
	if err != common.ErrKeyNotFound {
		t.Errorf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

// ── Tombstone GC removes expired tombstones ──────────────────────────────────

func TestTombstoneGCRemovesExpired(t *testing.T) {
	opts := DefaultStorageOptions(t.TempDir(), "node1")
	opts.SyncWrites = false
	opts.BlockCacheSize = 8 << 20
	opts.MemTableSize = 8 << 20
	opts.TombstoneGCEnabled = false
	opts.TombstoneTTL = 0 // Expire immediately
	s, err := NewStorage(opts)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	defer s.Close()

	key := []byte("key")
	vc := makeVC("node1", 1)

	// Write tombstone with old timestamp
	tombstone := Sibling{
		Value:     []byte{},
		VClock:    vc,
		Timestamp: time.Now().Unix() - 100, // old
		Tombstone: true,
	}
	if err := s.Put(key, &SiblingSet{Siblings: []Sibling{tombstone}}); err != nil {
		t.Fatalf("Put tombstone: %v", err)
	}

	// Verify tombstone is stored (raw)
	raw, err := s.GetRaw(key)
	if err != nil {
		t.Fatalf("GetRaw before GC: %v", err)
	}
	if len(raw.Siblings) == 0 || !raw.Siblings[0].Tombstone {
		t.Fatal("expected tombstone sibling before GC")
	}

	// Run GC
	s.runTombstoneGC()

	// After GC, the key should be gone
	_, err = s.GetRaw(key)
	if err != common.ErrKeyNotFound {
		t.Errorf("expected ErrKeyNotFound after GC, got %v", err)
	}
}

// ── GetRaw exposes tombstones ────────────────────────────────────────────────

func TestGetRawExposesTombstones(t *testing.T) {
	s := testStorage(t)
	key := []byte("key")

	vc := makeVC("node1", 1)
	s.Put(key, &SiblingSet{Siblings: []Sibling{
		{Value: []byte("val"), VClock: vc, Timestamp: 1},
	}})

	vcDel := vc.Copy()
	vcDel.SetString("node1", 2)
	s.Delete(key, vcDel)

	// Get returns NotFound (tombstone filtered)
	_, err := s.Get(key)
	if err != common.ErrKeyNotFound {
		t.Errorf("Get should return NotFound; got %v", err)
	}

	// GetRaw returns the tombstone
	raw, err := s.GetRaw(key)
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	hasTombstone := false
	for _, sib := range raw.Siblings {
		if sib.Tombstone {
			hasTombstone = true
		}
	}
	if !hasTombstone {
		t.Error("GetRaw should expose tombstone sibling")
	}
}

// ── Scan iterates all keys ────────────────────────────────────────────────────

func TestScan(t *testing.T) {
	s := testStorage(t)

	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		vc := makeVC("node1", 1)
		s.Put([]byte(k), &SiblingSet{Siblings: []Sibling{
			{Value: []byte("v"), VClock: vc, Timestamp: 1},
		}})
	}

	found := 0
	err := s.Scan(nil, nil, func(key []byte, _ *SiblingSet) bool {
		found++
		return true
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if found != len(keys) {
		t.Errorf("expected %d keys from Scan, got %d", len(keys), found)
	}
}
