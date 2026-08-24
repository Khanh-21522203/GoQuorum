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

// RecordEncodedLen returns the exact total byte size needed to encode a record with the given key and value lengths.
func RecordEncodedLen(keyLen, valLen int) int {
	return recordHeaderSize + keyLen + valLen
}

// EncodeRecordTo serializes key and val directly into dst in a single pass without heap allocations.
// dst must have cap(dst) >= RecordEncodedLen(len(key), len(val)).
//
// 0               4          8          10              10+KeyLen
// ┌───────────────┬──────────┬──────────┬───────────────┬──────────────────────┐
// │ Length uint32 │  CRC32   │  KeyLen  │      Key      │    Value (Payload)   │
// │ (Excl Length) │ (uint32) │ (uint16) │ (KeyLen bytes)│   (variable bytes)   │
// └───────────────┴──────────┴──────────┴───────────────┴──────────────────────┘
func EncodeRecordTo(dst []byte, key []byte, val []byte) ([]byte, error) {
	if len(key) > math.MaxUint16 {
		return nil, fmt.Errorf("journal: key too long to encode: %d bytes", len(key))
	}

	totalLen := RecordEncodedLen(len(key), len(val))
	if cap(dst) < totalLen {
		dst = make([]byte, totalLen)
	} else {
		dst = dst[:totalLen]
	}

	// 1. Length field (covers CRC + KeyLen + Key + Value)
	length := uint32(totalLen - recordLengthFieldSize)
	binary.BigEndian.PutUint32(dst[0:4], length)

	// 2. KeyLen field
	binary.BigEndian.PutUint16(dst[8:10], uint16(len(key)))

	// 3. Key and Value payloads
	keyEnd := 10 + len(key)
	copy(dst[10:keyEnd], key)
	copy(dst[keyEnd:totalLen], val)

	// 4. IEEE CRC32 calculated in-place over KeyLen + Key + Value
	crc := crc32.ChecksumIEEE(dst[8:totalLen])
	binary.BigEndian.PutUint32(dst[4:8], crc)

	return dst[:totalLen], nil
}

// EncodeRecord serializes a raw key and value into a newly allocated binary WAL record.
func EncodeRecord(key []byte, val []byte) ([]byte, error) {
	totalLen := RecordEncodedLen(len(key), len(val))
	dst := make([]byte, totalLen)
	return EncodeRecordTo(dst, key, val)
}

// DecodeRecord parses a record from data and returns direct zero-copy subslices for key and value.
// The returned key and val slices point directly into data without memory allocations.
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

	// Zero-copy subslice views
	k := rest[:keyLen]
	v := rest[keyLen:]

	return k, v, int(total), nil
}
