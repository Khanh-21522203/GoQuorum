package journal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"

	"goquorum.io/v2/contracts/quorumerr"
)

// Header field sizes for WAL records.
const (
	recordLengthFieldSize = 4
	recordCRCFieldSize    = 4
	recordKeyLenFieldSize = 2
	recordHeaderSize      = recordLengthFieldSize + recordCRCFieldSize + recordKeyLenFieldSize
)

// EncodeRecord serializes a raw key and value into an on-disk binary WAL record.
//
// 0               4          8          10              10+KeyLen
// ┌───────────────┬──────────┬──────────┬───────────────┬──────────────────────┐
// │ Length uint32 │  CRC32   │  KeyLen  │      Key      │    Value (Payload)   │
// │ (Excl Length) │ (uint32) │ (uint16) │ (KeyLen bytes)│   (variable bytes)   │
// └───────────────┴──────────┴──────────┴───────────────┴──────────────────────┘
//
// Length covers everything after the Length field (CRC + KeyLen + Key + Value).
// CRC32 (IEEE) covers KeyLen + Key + Value to detect torn writes.
func EncodeRecord(key []byte, val []byte) ([]byte, error) {
	if len(key) > math.MaxUint16 {
		return nil, fmt.Errorf("journal: key too long to encode: %d bytes", len(key))
	}

	body := make([]byte, 0, recordKeyLenFieldSize+len(key)+len(val))
	body = binary.BigEndian.AppendUint16(body, uint16(len(key)))
	body = append(body, key...)
	body = append(body, val...)

	crc := crc32.ChecksumIEEE(body)
	length := uint32(recordCRCFieldSize + len(body))

	record := make([]byte, 0, recordLengthFieldSize+int(length))
	record = binary.BigEndian.AppendUint32(record, length)
	record = binary.BigEndian.AppendUint32(record, crc)
	record = append(record, body...)
	return record, nil
}

// DecodeRecord parses a record from data. Returns the raw key, value, total bytes consumed, or error.
func DecodeRecord(data []byte) (key []byte, val []byte, consumed int, err error) {
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

	body := data[recordLengthFieldSize:total]
	if len(body) < recordCRCFieldSize+recordKeyLenFieldSize {
		return nil, nil, 0, fmt.Errorf("%w: record header truncated", quorumerr.ErrCorruptedData)
	}
	wantCRC := binary.BigEndian.Uint32(body)
	rest := body[recordCRCFieldSize:]

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
	v := make([]byte, len(rest)-int(keyLen))
	copy(v, rest[keyLen:])

	return k, v, int(total), nil
}
