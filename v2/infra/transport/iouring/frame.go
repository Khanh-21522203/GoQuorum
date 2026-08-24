package iouring

import (
	"encoding/binary"
	"errors"
	"fmt"

	"goquorum.io/v2/contracts/quorumerr"
)

// FrameHeaderSize is the fixed size of a frame header on the wire (16 bytes).
const FrameHeaderSize = 16

// MaxFramePayloadSize bounds the size of a single frame's payload to 16 MB.
const MaxFramePayloadSize = 16 * 1024 * 1024

// Wire errors.
var (
	ErrFrameTooLarge  = errors.New("iouring: frame payload exceeds MaxFramePayloadSize")
	ErrFrameTruncated = errors.New("iouring: frame header truncated")
)

// FrameHeader is the fixed 16-byte header prepended to every wire frame.
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                      Total Length (4 Bytes)                   |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|       Message ID (2 Bytes)    |     Schema Version (2 Bytes)  |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                                                               |
//	+                    Correlation ID (8 Bytes)                   +
//	|                                                               |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                                                               |
//	+                    Raw Binary Payload (N Bytes)               +
//	|                  (Key, Vector Clock, Siblings)                |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
type FrameHeader struct {
	TotalLength   uint32
	MessageID     uint16
	SchemaVersion uint16
	CorrelationID uint64
}

// EncodeFrameTo serializes the header and body into dst in a single pass without heap allocations.
func EncodeFrameTo(dst []byte, msgID uint16, schemaVersion uint16, correlationID uint64, body []byte) []byte {
	totalLen := uint32(FrameHeaderSize + len(body))
	if cap(dst) < int(totalLen) {
		dst = make([]byte, totalLen)
	} else {
		dst = dst[:totalLen]
	}

	binary.BigEndian.PutUint32(dst[0:4], totalLen)
	binary.BigEndian.PutUint16(dst[4:6], msgID)
	binary.BigEndian.PutUint16(dst[6:8], schemaVersion)
	binary.BigEndian.PutUint64(dst[8:16], correlationID)

	if len(body) > 0 {
		// Only copy if body isn't already positioned at dst[16:]
		if len(dst) >= FrameHeaderSize+len(body) && &dst[FrameHeaderSize] != &body[0] {
			copy(dst[FrameHeaderSize:], body)
		}
	}
	return dst
}

// DecodeFrame parses a framed message from data and returns the header and a zero-copy subslice view of body.
// The returned body points directly into data without memory allocations.
func DecodeFrame(data []byte) (hdr FrameHeader, body []byte, consumed int, err error) {
	if len(data) < FrameHeaderSize {
		return FrameHeader{}, nil, 0, fmt.Errorf("%w: frame header truncated: have %d bytes, need %d",
			quorumerr.ErrCorruptedData, len(data), FrameHeaderSize)
	}
	totalLen := binary.BigEndian.Uint32(data[0:4])
	if totalLen < FrameHeaderSize || totalLen > MaxFramePayloadSize+FrameHeaderSize {
		return FrameHeader{}, nil, 0, fmt.Errorf("%w: invalid total length %d", quorumerr.ErrCorruptedData, totalLen)
	}
	if len(data) < int(totalLen) {
		return FrameHeader{}, nil, 0, fmt.Errorf("%w: frame truncated: have %d bytes, need %d",
			quorumerr.ErrCorruptedData, len(data), totalLen)
	}

	hdr = FrameHeader{
		TotalLength:   totalLen,
		MessageID:     binary.BigEndian.Uint16(data[4:6]),
		SchemaVersion: binary.BigEndian.Uint16(data[6:8]),
		CorrelationID: binary.BigEndian.Uint64(data[8:16]),
	}
	body = data[FrameHeaderSize:totalLen]
	return hdr, body, int(totalLen), nil
}
