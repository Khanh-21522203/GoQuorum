package vclock

import (
	"GoQuorum/internal/common"
	"testing"
	"time"
)

func TestVectorClockTick(t *testing.T) {
	vc := NewVectorClock()

	// Tick node1
	vc.Tick("node1")
	if vc.Get("node1") != 1 {
		t.Errorf("Expected 1, got %d", vc.Get("node1"))
	}

	// Tick again
	vc.Tick("node1")
	if vc.Get("node1") != 2 {
		t.Errorf("Expected 2, got %d", vc.Get("node1"))
	}

	// Tick different node
	vc.Tick("node2")
	if vc.Get("node2") != 1 {
		t.Errorf("Expected 1, got %d", vc.Get("node2"))
	}
}

func TestVectorClockMerge(t *testing.T) {
	vc1 := NewVectorClock()
	vc1.Set("node1", 3)
	vc1.Set("node2", 1)

	vc2 := NewVectorClock()
	vc2.Set("node1", 2)
	vc2.Set("node2", 2)
	vc2.Set("node3", 1)

	vc1.Merge(vc2)

	// Should take max of each
	if vc1.Get("node1") != 3 {
		t.Errorf("Expected 3, got %d", vc1.Get("node1"))
	}
	if vc1.Get("node2") != 2 {
		t.Errorf("Expected 2, got %d", vc1.Get("node2"))
	}
	if vc1.Get("node3") != 1 {
		t.Errorf("Expected 1, got %d", vc1.Get("node3"))
	}
}

func TestVectorClockPrune(t *testing.T) {
	vc := NewVectorClock()

	// Add 10 entries with different timestamps
	now := time.Now().Unix()
	for i := 0; i < 10; i++ {
		nodeID := common.NodeID(string(rune('a' + i)))
		vc.entries[nodeID] = &VectorClockEntry{
			NodeID:    nodeID,
			Counter:   uint64(i + 1),
			Timestamp: now - int64(i)*86400, // Each 1 day older
		}
	}

	// Prune entries older than 5 days, max 3 entries
	pruned := vc.Prune(5*24*time.Hour, 3)

	if pruned <= 0 {
		t.Error("Expected some entries to be pruned")
	}

	if vc.Size() > 3 {
		t.Errorf("Expected max 3 entries, got %d", vc.Size())
	}
}

func TestVectorClockBinaryEncoding(t *testing.T) {
	vc1 := NewVectorClock()
	vc1.Tick("node1")
	vc1.Tick("node1")
	vc1.Tick("node2")

	// Encode
	data, err := vc1.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}

	// Decode
	vc2 := NewVectorClock()
	err = vc2.UnmarshalBinary(data)
	if err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}

	// Compare
	if vc1.Compare(vc2) != Equal {
		t.Error("Round-trip encoding changed vector clock")
	}
}
