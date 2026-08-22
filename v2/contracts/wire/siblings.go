package wire

import (
	"encoding/binary"
	"fmt"
	"math"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
)

// StorageSibling is a single conflicting version of a value stored under a key.
type StorageSibling struct {
	Value     []byte
	VClock    vclock.VectorClock
	Timestamp int64 // Unix timestamp (seconds).
	Tombstone bool
	ExpiresAt int64 // Unix timestamp (seconds); 0 means no TTL.
}

// SiblingSet is the full set of siblings stored under a single key.
type SiblingSet struct {
	Siblings []StorageSibling
}

// AppendMarshalBinary encodes the sibling set to dst in compact binary format.
func (ss SiblingSet) AppendMarshalBinary(dst []byte) ([]byte, error) {
	if len(ss.Siblings) > math.MaxUint16 {
		return nil, fmt.Errorf("wire: too many siblings to encode: %d", len(ss.Siblings))
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
		return nil, fmt.Errorf("wire: too many siblings to encode: %d", len(ss.Siblings))
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
		ss.Siblings = make([]StorageSibling, 0, count)
	}

	for i := uint16(0); i < count; i++ {
		var (
			s   StorageSibling
			err error
		)
		s, data, err = decodeStorageSibling(data)
		if err != nil {
			return err
		}
		ss.Siblings = append(ss.Siblings, s)
	}
	return nil
}

func decodeStorageSibling(data []byte) (StorageSibling, []byte, error) {
	var s StorageSibling

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
