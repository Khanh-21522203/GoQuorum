package iouring

import (
	"encoding/binary"
	"fmt"

	"goquorum.io/v2/contracts/quorumerr"
)

// FrameHeaderSize is the fixed byte size of a frame header.
const FrameHeaderSize = 16

// FrameHeader is the fixed 16-byte prefix for all wire frames.
//
// 0               4          6              8                     16
// ┌───────────────┬──────────┬──────────────┬──────────────────────┐
// │  TotalLength  │MessageID │SchemaVersion │    CorrelationID     │
// │   (uint32)    │ (uint16) │   (uint16)   │       (uint64)       │
// └───────────────┴──────────┴──────────────┴──────────────────────┘
//
// TotalLength includes the 16 header bytes plus the variable-length body.
type FrameHeader struct {
	TotalLength   uint32
	MessageID     MessageID
	SchemaVersion uint16
	CorrelationID uint64
}

// EncodeFrame serializes a frame header and body into wire format.
func EncodeFrame(msgID uint16, schemaVersion uint16, correlationID uint64, body []byte) []byte {
	total := FrameHeaderSize + len(body)
	buf := make([]byte, FrameHeaderSize, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	binary.BigEndian.PutUint16(buf[4:6], msgID)
	binary.BigEndian.PutUint16(buf[6:8], schemaVersion)
	binary.BigEndian.PutUint64(buf[8:16], correlationID)
	buf = append(buf, body...)
	return buf
}

// DecodeFrameHeader parses the 16-byte header from the prefix of data.
func DecodeFrameHeader(data []byte) (FrameHeader, error) {
	if len(data) < FrameHeaderSize {
		return FrameHeader{}, fmt.Errorf("%w: frame header truncated: need %d bytes, have %d", quorumerr.ErrCorruptedData, FrameHeaderSize, len(data))
	}
	return FrameHeader{
		TotalLength:   binary.BigEndian.Uint32(data[0:4]),
		MessageID:     MessageID(binary.BigEndian.Uint16(data[4:6])),
		SchemaVersion: binary.BigEndian.Uint16(data[6:8]),
		CorrelationID: binary.BigEndian.Uint64(data[8:16]),
	}, nil
}

// Reassembler buffers incoming TCP byte chunks and reconstructs discrete frames.
// Not safe for concurrent use.
type Reassembler struct {
	buf []byte
}

// Feed appends a newly received TCP byte chunk to the accumulation buffer.
func (r *Reassembler) Feed(chunk []byte) {
	r.buf = append(r.buf, chunk...)
}

// Next pops the next complete frame from the buffer, if available.
func (r *Reassembler) Next() (FrameHeader, []byte, bool) {
	if len(r.buf) < FrameHeaderSize {
		return FrameHeader{}, nil, false
	}
	hdr, err := DecodeFrameHeader(r.buf)
	if err != nil || hdr.TotalLength < FrameHeaderSize {
		return FrameHeader{}, nil, false
	}
	if uint32(len(r.buf)) < hdr.TotalLength {
		return FrameHeader{}, nil, false
	}

	body := make([]byte, hdr.TotalLength-FrameHeaderSize)
	copy(body, r.buf[FrameHeaderSize:hdr.TotalLength])

	remaining := len(r.buf) - int(hdr.TotalLength)
	newBuf := make([]byte, remaining)
	copy(newBuf, r.buf[hdr.TotalLength:])
	r.buf = newBuf

	return hdr, body, true
}
