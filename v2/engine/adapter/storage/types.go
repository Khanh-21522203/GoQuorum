package storage

import (
	"bytes"

	"goquorum.io/v2/contracts/wire"
)

// Sibling is a single conflicting version of a value stored under a key.
type Sibling = wire.StorageSibling

// SiblingSet is the full set of siblings stored under a single key.
type SiblingSet = wire.SiblingSet

// ScanFunc is invoked for each key visited by Storage.Scan. Returning false
// stops the scan early.
type ScanFunc func(key []byte, siblings *SiblingSet) bool

// Stats reports point-in-time storage engine statistics.
type Stats struct {
	KeyCount        int64
	SizeBytes       uint64
	L0FileCount     int64
	CompactionCount int64
	WALBytesWritten uint64
}

// Reconcile merges two sibling sets, keeping only the causally maximal (undominated) siblings.
func Reconcile(a, b *SiblingSet) *SiblingSet {
	if a == nil || len(a.Siblings) == 0 {
		return b
	}
	if b == nil || len(b.Siblings) == 0 {
		return a
	}

	all := make([]Sibling, 0, len(a.Siblings)+len(b.Siblings))
	all = append(all, a.Siblings...)
	all = append(all, b.Siblings...)

	maximal := make([]Sibling, 0, len(all))
	for i, s := range all {
		dominated := false
		for j, other := range all {
			if i == j {
				continue
			}
			if other.VClock.Dominates(s.VClock) && !other.VClock.Equals(s.VClock) {
				dominated = true
				break
			}
		}
		if dominated {
			continue
		}
		duplicate := false
		for _, m := range maximal {
			if m.VClock.Equals(s.VClock) && bytes.Equal(m.Value, s.Value) && m.Tombstone == s.Tombstone {
				duplicate = true
				break
			}
		}
		if !duplicate {
			maximal = append(maximal, s)
		}
	}
	return &SiblingSet{Siblings: maximal}
}
