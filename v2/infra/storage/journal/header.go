package journal

import (
	"bytes"
	"encoding/binary"
)

// SegmentStatus indicates the lifecycle role of a segment in the circular ring.
type SegmentStatus uint8

const (
	// StatusEmpty indicates an uninitialized or clean/dormant segment.
	StatusEmpty SegmentStatus = 0
	// StatusWriter indicates a segment written or closed by client append writes.
	StatusWriter SegmentStatus = 1
	// StatusCompacted indicates a baseline checkpoint segment created by compaction.
	StatusCompacted SegmentStatus = 2
)

const (
	// SegmentHeaderSize is the fixed byte size of a segment file header (16 bytes, 8-byte aligned).
	SegmentHeaderSize = 16
)

var (
	// SegmentMagic is the 4-byte identifier stamped at the start of every valid segment file.
	SegmentMagic = [4]byte{'Q', 'U', 'O', 'R'}
)

// EncodeSegmentHeader encodes a 16-byte segment header containing the magic bytes, monotonic epoch, and status.
// Layout: [Magic: 4B ("QUOR")][Epoch: 8B uint64 (BigEndian)][Status: 1B][Reserved: 3B (0)]
func EncodeSegmentHeader(epoch uint64, status SegmentStatus) []byte {
	buf := make([]byte, SegmentHeaderSize)
	copy(buf[0:4], SegmentMagic[:])
	binary.BigEndian.PutUint64(buf[4:12], epoch)
	buf[12] = byte(status)
	return buf
}

// DecodeSegmentHeader parses a 16-byte segment header, validating magic bytes and returning the epoch and status.
func DecodeSegmentHeader(buf []byte) (epoch uint64, status SegmentStatus, ok bool) {
	if len(buf) < SegmentHeaderSize {
		return 0, 0, false
	}
	if !bytes.Equal(buf[0:4], SegmentMagic[:]) {
		return 0, 0, false
	}
	epoch = binary.BigEndian.Uint64(buf[4:12])
	status = SegmentStatus(buf[12])
	return epoch, status, true
}
