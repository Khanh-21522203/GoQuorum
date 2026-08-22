package pool

import (
	"math/bits"
)

// Slot represents a single pre-allocated slot holding an in-flight item of type T.
type Slot[T any] struct {
	ID     uint64 // Request ID / Correlation ID
	Active bool   // In-use flag
	Value  T      // Inline value payload (Zero heap pointers!)
}

// SlotTable is a power-of-two, contiguous slot table designed for high-throughput,
// zero-allocation in-flight request tracking in single-threaded / thread-confined reactors.
type SlotTable[T any] struct {
	slots []Slot[T]
	mask  uint64
	cap   int
}

// NewSlotTable creates a contiguous SlotTable with capacity rounded up to the next power of two (min 16).
func NewSlotTable[T any](capacity int) *SlotTable[T] {
	if capacity < 16 {
		capacity = 16
	}
	// Round capacity up to next power of two
	capPow2 := 1 << bits.Len(uint(capacity-1))
	return &SlotTable[T]{
		slots: make([]Slot[T], capPow2),
		mask:  uint64(capPow2 - 1),
		cap:   capPow2,
	}
}

// Capacity returns the total number of pre-allocated slots in the table.
func (st *SlotTable[T]) Capacity() int {
	return st.cap
}

// Acquire reserves the slot mapped to id and returns a pointer to the Slot[T].
func (st *SlotTable[T]) Acquire(id uint64) *Slot[T] {
	slot := &st.slots[id&st.mask]
	slot.ID = id
	slot.Active = true
	return slot
}

// Get returns the slot mapped to id if active and matching.
func (st *SlotTable[T]) Get(id uint64) (*Slot[T], bool) {
	slot := &st.slots[id&st.mask]
	if slot.Active && slot.ID == id {
		return slot, true
	}
	return nil, false
}

// Release marks the slot as inactive if the ID matches.
func (st *SlotTable[T]) Release(id uint64) {
	slot := &st.slots[id&st.mask]
	if slot.ID == id {
		slot.Active = false
	}
}

// Reset clears all active slots in the table.
func (st *SlotTable[T]) Reset() {
	var zero T
	for i := range st.slots {
		st.slots[i].Active = false
		st.slots[i].ID = 0
		st.slots[i].Value = zero
	}
}

// ForEach iterates over all currently active slots.
func (st *SlotTable[T]) ForEach(fn func(id uint64, slot *Slot[T])) {
	for i := range st.slots {
		if st.slots[i].Active {
			fn(st.slots[i].ID, &st.slots[i])
		}
	}
}
