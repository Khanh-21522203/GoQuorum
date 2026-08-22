package pool

import (
	"testing"
)

type testPayload struct {
	name   [32]byte
	length int
	val    uint64
}

func TestSlotTable_AcquireGetRelease(t *testing.T) {
	st := NewSlotTable[testPayload](100) // Will round up to 128
	if st.Capacity() != 128 {
		t.Fatalf("Capacity = %d, want 128", st.Capacity())
	}

	reqID := uint64(1042)
	slot := st.Acquire(reqID)
	slot.Value.val = 9999
	copy(slot.Value.name[:], "request-payload")

	// Get slot
	got, ok := st.Get(reqID)
	if !ok {
		t.Fatal("expected Get to find active slot")
	}
	if got.Value.val != 9999 {
		t.Fatalf("got val = %d, want 9999", got.Value.val)
	}

	// Mismatched ID should not match
	if _, ok := st.Get(reqID + 128); ok {
		t.Fatal("expected Get with mismatched ID to fail")
	}

	// Release slot
	st.Release(reqID)
	if _, ok := st.Get(reqID); ok {
		t.Fatal("expected Get after Release to return false")
	}
}

func TestSlotTable_ZeroAllocations(t *testing.T) {
	st := NewSlotTable[testPayload](4096)

	var reqID uint64 = 1
	allocs := testing.AllocsPerRun(100, func() {
		reqID++
		slot := st.Acquire(reqID)
		slot.Value.val = reqID * 2
		_, _ = st.Get(reqID)
		st.Release(reqID)
	})

	if allocs != 0 {
		t.Fatalf("SlotTable Acquire/Get/Release allocated %f objects, want 0", allocs)
	}
}
