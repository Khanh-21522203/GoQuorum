package storage

import (
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

// MarshalBinary encodes the sibling set to a compact binary format, shared
// by every storage and transport adapter so this encoding exists in
// exactly one place.
//
// Layout: [count uint16]{[valueLen uint32][value][vclockLen uint32][vclock]
// [timestamp int64][tombstone byte][expiresAt int64]}*count
func (ss SiblingSet) MarshalBinary() ([]byte, error) {
	if len(ss.Siblings) > math.MaxUint16 {
		return nil, fmt.Errorf("storage: too many siblings to encode: %d", len(ss.Siblings))
	}
	buf := make([]byte, 2, 64*len(ss.Siblings)+2)
	binary.BigEndian.PutUint16(buf, uint16(len(ss.Siblings)))
	for _, s := range ss.Siblings {
		vc, err := s.VClock.MarshalBinary()
		if err != nil {
			return nil, err
		}
		buf = appendUint32Prefixed(buf, s.Value)
		buf = appendUint32Prefixed(buf, vc)
		buf = binary.BigEndian.AppendUint64(buf, uint64(s.Timestamp))
		if s.Tombstone {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
		buf = binary.BigEndian.AppendUint64(buf, uint64(s.ExpiresAt))
	}
	return buf, nil
}

// UnmarshalBinary decodes a sibling set previously produced by
// MarshalBinary. It never panics on truncated or malformed input, since it
// must safely reject data from an untrusted peer or a torn disk write.
func (ss *SiblingSet) UnmarshalBinary(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: sibling set header truncated", quorumerr.ErrCorruptedData)
	}
	count := binary.BigEndian.Uint16(data)
	data = data[2:]

	siblings := make([]Sibling, 0, count)
	for i := uint16(0); i < count; i++ {
		var (
			s   Sibling
			err error
		)
		s, data, err = decodeSibling(data)
		if err != nil {
			return err
		}
		siblings = append(siblings, s)
	}
	ss.Siblings = siblings
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
