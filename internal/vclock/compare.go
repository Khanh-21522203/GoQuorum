package vclock

import "GoQuorum/internal/common"

// Ordering represents the relationship between two vector clocks
type Ordering int

const (
	Before     Ordering = iota // vc1 < vc2 (vc1 happens before vc2)
	After                      // vc1 > vc2 (vc1 happens after vc2)
	Equal                      // vc1 == vc2 (identical)
	Concurrent                 // Neither dominates (conflict)
)

func (o Ordering) String() string {
	switch o {
	case Before:
		return "BEFORE"
	case After:
		return "AFTER"
	case Equal:
		return "EQUAL"
	case Concurrent:
		return "CONCURRENT"
	default:
		return "UNKNOWN"
	}
}

// Compare compares two vector clocks (Section 4.2 - Compare operation)
// Returns the causal relationship between them
//
// Algorithm:
//  1. For all nodes in union of both clocks
//  2. Track if any counter in vc1 < vc2 (less = true)
//  3. Track if any counter in vc1 > vc2 (greater = true)
//  4. Determine relationship:
//     - less && !greater   → BEFORE (vc1 < vc2)
//     - greater && !less   → AFTER (vc1 > vc2)
//     - !less && !greater  → EQUAL (vc1 == vc2)
//     - less && greater    → CONCURRENT (conflict)
//
// Examples:
//
//	Compare({A:1}, {A:2})           → BEFORE
//	Compare({A:2}, {A:1})           → AFTER
//	Compare({A:1}, {A:1})           → EQUAL
//	Compare({A:2}, {A:1,B:1})       → CONCURRENT
//	Compare({A:1,B:2}, {A:2,B:1})   → CONCURRENT
func (vc VectorClock) Compare(other VectorClock) Ordering {
	less := false
	greater := false

	// Collect all nodes from both clocks
	allNodes := make(map[common.NodeID]bool)
	for nodeID := range vc.entries {
		allNodes[nodeID] = true
	}
	for nodeID := range other.entries {
		allNodes[nodeID] = true
	}

	// Compare counters for all nodes
	for nodeID := range allNodes {
		v1 := vc.Get(nodeID)
		v2 := other.Get(nodeID)

		if v1 < v2 {
			less = true
		}
		if v1 > v2 {
			greater = true
		}
	}

	// Determine relationship (Section 4.2)
	if less && !greater {
		return Before // vc1 < vc2
	}
	if greater && !less {
		return After // vc1 > vc2
	}
	if !less && !greater {
		return Equal // vc1 == vc2
	}
	return Concurrent // Neither dominates
}

// HappensAfter returns true if this vector clock happens after other
// Equivalent to: vc.Compare(other) == After
func (vc VectorClock) HappensAfter(other VectorClock) bool {
	return vc.Compare(other) == After
}

// HappensBefore returns true if this vector clock happens before other
// Equivalent to: vc.Compare(other) == Before
func (vc VectorClock) HappensBefore(other VectorClock) bool {
	return vc.Compare(other) == Before
}

// IsConcurrentWith returns true if neither dominates
// Equivalent to: vc.Compare(other) == Concurrent
func (vc VectorClock) IsConcurrentWith(other VectorClock) bool {
	return vc.Compare(other) == Concurrent
}

// Equals returns true if vector clocks are identical
// Equivalent to: vc.Compare(other) == Equal
func (vc VectorClock) Equals(other VectorClock) bool {
	return vc.Compare(other) == Equal
}

// Dominates returns true if vc happens after or equals other
// Used for checking if a version supersedes another
func (vc VectorClock) Dominates(other VectorClock) bool {
	ordering := vc.Compare(other)
	return ordering == After || ordering == Equal
}

// IsDominatedBy returns true if other happens after or equals vc
// Inverse of Dominates
func (vc VectorClock) IsDominatedBy(other VectorClock) bool {
	ordering := vc.Compare(other)
	return ordering == Before || ordering == Equal
}

// IsStrictlyBefore returns true if vc < other (not equal)
func (vc VectorClock) IsStrictlyBefore(other VectorClock) bool {
	return vc.Compare(other) == Before
}

// IsStrictlyAfter returns true if vc > other (not equal)
func (vc VectorClock) IsStrictlyAfter(other VectorClock) bool {
	return vc.Compare(other) == After
}

// CanCauseConflict returns true if merging these clocks would create siblings
// This happens when they are concurrent or equal but with different values
func (vc VectorClock) CanCauseConflict(other VectorClock) bool {
	ordering := vc.Compare(other)
	return ordering == Concurrent || ordering == Equal
}
