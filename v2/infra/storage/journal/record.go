package journal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/storage"
)

// recordLengthFieldSize, recordCRCFieldSize and recordKeyLenFieldSize are
// the sizes, in bytes, of the three fixed-size fields at the front of every
// record. See the package doc's "On-disk format" description for the full
// layout.
const (
	recordLengthFieldSize = 4
	recordCRCFieldSize    = 4
	recordKeyLenFieldSize = 2
	recordHeaderSize      = recordLengthFieldSize + recordCRCFieldSize + recordKeyLenFieldSize
)

// EncodeRecord serializes key and siblings into a single self-describing
// record.
//
// On-disk layout:
//
//	Length uint32 | CRC32 uint32 | KeyLen uint16 | key (KeyLen bytes) | siblings (SiblingSet.MarshalBinary())
//
// Length is the number of bytes that follow the Length field itself, i.e.
// CRC32 + KeyLen + key + siblings, so a reader can locate the next record
// without decoding this one. CRC32 (crc32.ChecksumIEEE) covers everything
// after the CRC32 field itself (KeyLen + key + siblings), so a torn or
// otherwise corrupted write is detectable.
func EncodeRecord(key []byte, siblings *storage.SiblingSet) ([]byte, error) {
	if len(key) > math.MaxUint16 {
		return nil, fmt.Errorf("journal: key too long to encode: %d bytes", len(key))
	}
	var siblingBytes []byte
	if siblings != nil {
		encoded, err := siblings.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("journal: encoding sibling set: %w", err)
		}
		siblingBytes = encoded
	} else {
		encoded, err := (storage.SiblingSet{}).MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("journal: encoding empty sibling set: %w", err)
		}
		siblingBytes = encoded
	}

	// body is everything the CRC32 field covers: KeyLen + key + siblings.
	body := make([]byte, 0, recordKeyLenFieldSize+len(key)+len(siblingBytes))
	body = binary.BigEndian.AppendUint16(body, uint16(len(key)))
	body = append(body, key...)
	body = append(body, siblingBytes...)

	crc := crc32.ChecksumIEEE(body)
	length := uint32(recordCRCFieldSize + len(body))

	record := make([]byte, 0, recordLengthFieldSize+int(length))
	record = binary.BigEndian.AppendUint32(record, length)
	record = binary.BigEndian.AppendUint32(record, crc)
	record = append(record, body...)
	return record, nil
}

// DecodeRecord decodes a single record previously produced by
// EncodeRecord from the front of data. consumed is the total number of
// bytes read, including the header, so a sequential reader can advance
// straight to the next record. DecodeRecord never panics on truncated or
// corrupt input: it always returns a quorumerr.ErrCorruptedData-wrapped
// error instead, leaving it to the caller (see replay.go) to decide
// whether a truncated final record is an expected, benign end-of-log
// condition rather than a fatal error.
func DecodeRecord(data []byte) (key []byte, siblings *storage.SiblingSet, consumed int, err error) {
	if len(data) < recordLengthFieldSize {
		return nil, nil, 0, fmt.Errorf("%w: record length header truncated: have %d bytes, need %d",
			quorumerr.ErrCorruptedData, len(data), recordLengthFieldSize)
	}
	length := binary.BigEndian.Uint32(data)
	total := int64(recordLengthFieldSize) + int64(length)
	if int64(len(data)) < total {
		return nil, nil, 0, fmt.Errorf("%w: record truncated: have %d bytes, need %d",
			quorumerr.ErrCorruptedData, len(data), total)
	}

	// body is everything after the Length field: CRC32 + KeyLen + key + siblings.
	body := data[recordLengthFieldSize:total]
	if len(body) < recordCRCFieldSize+recordKeyLenFieldSize {
		return nil, nil, 0, fmt.Errorf("%w: record header truncated", quorumerr.ErrCorruptedData)
	}
	wantCRC := binary.BigEndian.Uint32(body)
	rest := body[recordCRCFieldSize:] // KeyLen + key + siblings; this is what CRC32 covers.

	gotCRC := crc32.ChecksumIEEE(rest)
	if gotCRC != wantCRC {
		return nil, nil, 0, fmt.Errorf("%w: record checksum mismatch: got %08x, want %08x",
			quorumerr.ErrCorruptedData, gotCRC, wantCRC)
	}

	keyLen := binary.BigEndian.Uint16(rest)
	rest = rest[recordKeyLenFieldSize:]
	if uint64(len(rest)) < uint64(keyLen) {
		return nil, nil, 0, fmt.Errorf("%w: record key truncated", quorumerr.ErrCorruptedData)
	}
	k := make([]byte, keyLen)
	copy(k, rest[:keyLen])
	siblingBytes := rest[keyLen:]

	var ss storage.SiblingSet
	if err := ss.UnmarshalBinary(siblingBytes); err != nil {
		return nil, nil, 0, fmt.Errorf("%w: decoding sibling set: %v", quorumerr.ErrCorruptedData, err)
	}

	return k, &ss, int(total), nil
}
