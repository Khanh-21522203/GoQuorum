package storage

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
)

// Sibling is a single conflicting version of a value stored under a key.
// Multiple siblings for the same key indicate an unresolved concurrent
// write (see vclock.Ordering).
type Sibling struct {
	Value     []byte
	VClock    vclock.VectorClock
	Timestamp int64 // Unix timestamp (seconds).
	Tombstone bool
	ExpiresAt int64 // Unix timestamp (seconds); 0 means no TTL.
}

// SiblingSet is the full set of siblings stored under a single key.
type SiblingSet struct {
	Siblings []Sibling
}

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

// AppendMarshalBinary encodes the sibling set to dst in compact binary format.
func (ss SiblingSet) AppendMarshalBinary(dst []byte) ([]byte, error) {
	if len(ss.Siblings) > math.MaxUint16 {
		return nil, fmt.Errorf("storage: too many siblings to encode: %d", len(ss.Siblings))
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(ss.Siblings)))
	for _, s := range ss.Siblings {
		dst = appendUint32Prefixed(dst, s.Value)

		vcOffset := len(dst)
		dst = append(dst, 0, 0, 0, 0) // reserve 4 bytes for length prefix
		var err error
		dst, err = s.VClock.AppendMarshalBinary(dst)
		if err != nil {
			return nil, err
		}
		vcLen := uint32(len(dst) - (vcOffset + 4))
		binary.BigEndian.PutUint32(dst[vcOffset:], vcLen)

		dst = binary.BigEndian.AppendUint64(dst, uint64(s.Timestamp))
		if s.Tombstone {
			dst = append(dst, 1)
		} else {
			dst = append(dst, 0)
		}
		dst = binary.BigEndian.AppendUint64(dst, uint64(s.ExpiresAt))
	}
	return dst, nil
}

// MarshalBinary encodes the sibling set to a compact binary format.
func (ss SiblingSet) MarshalBinary() ([]byte, error) {
	if len(ss.Siblings) > math.MaxUint16 {
		return nil, fmt.Errorf("storage: too many siblings to encode: %d", len(ss.Siblings))
	}
	buf := make([]byte, 0, 64*len(ss.Siblings)+2)
	return ss.AppendMarshalBinary(buf)
}

// UnmarshalBinary decodes a sibling set previously produced by MarshalBinary.
func (ss *SiblingSet) UnmarshalBinary(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: sibling set header truncated", quorumerr.ErrCorruptedData)
	}
	count := binary.BigEndian.Uint16(data)
	data = data[2:]

	if cap(ss.Siblings) >= int(count) {
		ss.Siblings = ss.Siblings[:0]
	} else {
		ss.Siblings = make([]Sibling, 0, count)
	}

	for i := uint16(0); i < count; i++ {
		var (
			s   Sibling
			err error
		)
		s, data, err = decodeSibling(data)
		if err != nil {
			return err
		}
		ss.Siblings = append(ss.Siblings, s)
	}
	return nil
}

func decodeSibling(data []byte) (Sibling, []byte, error) {
	var s Sibling

	value, rest, err := decodeUint32Prefixed(data)
	if err != nil {
		return s, nil, err
	}
	vcBytes, rest, err := decodeUint32Prefixed(rest)
	if err != nil {
		return s, nil, err
	}
	if err := s.VClock.UnmarshalBinary(vcBytes); err != nil {
		return s, nil, err
	}

	const trailerLen = 8 + 1 + 8 // timestamp + tombstone + expiresAt
	if len(rest) < trailerLen {
		return s, nil, fmt.Errorf("%w: sibling trailer truncated", quorumerr.ErrCorruptedData)
	}
	s.Value = value
	s.Timestamp = int64(binary.BigEndian.Uint64(rest))
	rest = rest[8:]
	s.Tombstone = rest[0] != 0
	rest = rest[1:]
	s.ExpiresAt = int64(binary.BigEndian.Uint64(rest))
	rest = rest[8:]
	return s, rest, nil
}

func appendUint32Prefixed(buf, data []byte) []byte {
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(data)))
	return append(buf, data...)
}

func decodeUint32Prefixed(data []byte) (value, rest []byte, err error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("%w: length prefix truncated", quorumerr.ErrCorruptedData)
	}
	n := binary.BigEndian.Uint32(data)
	data = data[4:]
	if uint64(n) > uint64(len(data)) {
		return nil, nil, fmt.Errorf("%w: length prefix exceeds remaining data", quorumerr.ErrCorruptedData)
	}
	return data[:n], data[n:], nil
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
