package iouring

import (
	"encoding/binary"
	"fmt"

	"goquorum.io/v2/contracts/quorumerr"
)

// FrameHeaderSize is the fixed size, in bytes, of the header prefixing
// every frame.
const FrameHeaderSize = 16

// FrameHeader is the fixed 16-byte header prefixing every frame exchanged
// over a persistent per-peer connection.
//
// Layout: [TotalLength uint32][MessageID uint16][SchemaVersion uint16]
// [CorrelationID uint64]
//
// TotalLength counts the ENTIRE frame, including these 16 header bytes.
// This is deliberate: a reader that only understands an older
// SchemaVersion can still locate the end of a frame produced by a newer
// sender that appended extra trailing fields to the body, and skip past
// those unknown bytes wholesale, without ever needing to understand the
// newer body layout. Framing (via TotalLength) and body decoding (via
// SchemaVersion-specific field layouts) are kept fully independent.
type FrameHeader struct {
	TotalLength   uint32
	MessageID     MessageID
	SchemaVersion uint16
	CorrelationID uint64
}

// EncodeFrame builds one complete frame: the 16-byte header followed by
// body. TotalLength is computed and set automatically.
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

// DecodeFrameHeader decodes the fixed 16-byte header from the front of
// data. data may contain more than just the header (the rest of the frame
// body, or even subsequent frames); only the first FrameHeaderSize bytes
// are consulted.
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

// Reassembler incrementally reconstructs complete frames from a stream of
// arbitrarily-chunked bytes, exactly as a raw socket recv would deliver
// them: a single chunk may contain a partial frame, several complete
// frames back to back, or a frame split across chunk boundaries anywhere
// (including inside the 16-byte header itself). Feed appends a new chunk;
// Next pops the next complete frame, if one is fully buffered. The zero
// value is ready to use.
//
// Reassembler is not safe for concurrent use.
type Reassembler struct {
	buf []byte
}

// Feed appends a newly-received chunk to the reassembly buffer.
func (r *Reassembler) Feed(chunk []byte) {
	r.buf = append(r.buf, chunk...)
}

// Next returns the next complete frame buffered so far, if any. On success
// it returns the decoded header, the frame body (the bytes after the
// 16-byte header, up to TotalLength), and ok=true, and retains any
// leftover bytes for the next Feed/Next cycle. If fewer than one complete
// frame is currently buffered, it returns ok=false and leaves the buffer
// untouched, so a later Feed can be followed by another Next call.
func (r *Reassembler) Next() (FrameHeader, []byte, bool) {
	if len(r.buf) < FrameHeaderSize {
		return FrameHeader{}, nil, false
	}
	hdr, err := DecodeFrameHeader(r.buf)
	if err != nil {
		// Unreachable given the length check above, but guard anyway
		// rather than assume.
		return FrameHeader{}, nil, false
	}
	if hdr.TotalLength < FrameHeaderSize {
		// A malformed/malicious header could never be a valid frame; there
		// is no safe way to locate the next frame boundary, so this
		// Reassembler simply refuses to make progress rather than panic or
		// guess.
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
