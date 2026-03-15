package storage

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/vclock"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const (
	// Magic number for record format (Section 5.1)
	MagicNumber uint32 = 0x47515243 // "GQRC"

	// Format version (Section 5.1)
	FormatVersion byte = 0x01

	// Flags (Section 5.2)
	FlagTombstone  byte = 1 << 0 // Bit 0: tombstone
	FlagCompressed byte = 1 << 1 // Bit 1: compressed (future)
	FlagTTL        byte = 1 << 2 // Bit 2: TTL set; ExpiresAt (8 bytes) follows timestamp

	// Limits (Section 2 & 3)
	MaxKeySize   = 65536   // 64 KB
	MaxValueSize = 1048576 // 1 MB
	MaxSiblings  = 65535   // uint16 max

	// Header size (Section 5.1)
	HeaderSize = 25
)

// encodeSiblingSet encodes siblings to binary format (Section 3.3)
// Format:
//
//	[sibling_count: 2 bytes]
//	For each sibling:
//	  [vclock_len: 4 bytes]
//	  [vclock: protobuf bytes]
//	  [flags: 1 byte]
//	  [timestamp: 8 bytes]
//	  [value_len: 4 bytes]
//	  [value: raw bytes]
//	[crc32: 4 bytes]
func encodeSiblingSet(set *SiblingSet) ([]byte, error) {
	if set == nil || len(set.Siblings) == 0 {
		return nil, fmt.Errorf("empty sibling set")
	}

	// Check limits (Section 3.1)
	if len(set.Siblings) > MaxSiblings {
		return nil, fmt.Errorf("too many siblings: %d (max: %d)",
			len(set.Siblings), MaxSiblings)
	}

	buf := new(bytes.Buffer)

	// Write sibling count (2 bytes) - Section 5.4
	if err := binary.Write(buf, binary.LittleEndian, uint16(len(set.Siblings))); err != nil {
		return nil, err
	}

	// Write each sibling
	for i, sib := range set.Siblings {
		// Validate value size (Section 3.1)
		if len(sib.Value) > MaxValueSize {
			return nil, fmt.Errorf("sibling %d value too large: %d bytes (max: %d)",
				i, len(sib.Value), MaxValueSize)
		}

		// Encode vector clock to bytes
		vclockBytes, err := sib.VClock.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("marshal vclock: %w", err)
		}

		// vclock_len (4 bytes)
		if err := binary.Write(buf, binary.LittleEndian, uint32(len(vclockBytes))); err != nil {
			return nil, err
		}

		// vclock bytes
		if _, err := buf.Write(vclockBytes); err != nil {
			return nil, err
		}

		// flags (1 byte) - Section 5.2
		flags := byte(0)
		if sib.Tombstone {
			flags |= FlagTombstone
		}
		if sib.ExpiresAt != 0 {
			flags |= FlagTTL
		}
		if err := buf.WriteByte(flags); err != nil {
			return nil, err
		}

		// timestamp (8 bytes) - Unix seconds (Section 5.1)
		if err := binary.Write(buf, binary.LittleEndian, sib.Timestamp); err != nil {
			return nil, err
		}

		// expires_at (8 bytes) — only present when FlagTTL is set
		if sib.ExpiresAt != 0 {
			if err := binary.Write(buf, binary.LittleEndian, sib.ExpiresAt); err != nil {
				return nil, err
			}
		}

		// value_len (4 bytes)
		if err := binary.Write(buf, binary.LittleEndian, uint32(len(sib.Value))); err != nil {
			return nil, err
		}

		// value bytes
		if _, err := buf.Write(sib.Value); err != nil {
			return nil, err
		}
	}

	// Calculate CRC32 for entire payload (Section 8.1)
	payload := buf.Bytes()
	crc := crc32.ChecksumIEEE(payload)

	// Append CRC32 (4 bytes)
	if err := binary.Write(buf, binary.LittleEndian, crc); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// decodeSiblingSet decodes binary format to siblings (Section 5.4)
func decodeSiblingSet(data []byte) (*SiblingSet, error) {
	if len(data) < 6 { // Min: 2 (count) + 4 (crc)
		return nil, common.ErrCorruptedData
	}

	// Verify CRC32 (Section 8.1)
	payloadLen := len(data) - 4
	payload := data[:payloadLen]
	storedCRC := binary.LittleEndian.Uint32(data[payloadLen:])
	calculatedCRC := crc32.ChecksumIEEE(payload)

	if storedCRC != calculatedCRC {
		return nil, fmt.Errorf("%w: CRC32 mismatch (stored=%08x, calculated=%08x)",
			common.ErrCorruptedData, storedCRC, calculatedCRC)
	}

	buf := bytes.NewReader(payload)

	// Read sibling count (2 bytes) - Section 5.4
	var siblingCount uint16
	if err := binary.Read(buf, binary.LittleEndian, &siblingCount); err != nil {
		return nil, fmt.Errorf("read sibling count: %w", err)
	}

	if siblingCount == 0 {
		return nil, fmt.Errorf("%w: zero sibling count", common.ErrCorruptedData)
	}
	if siblingCount > MaxSiblings {
		return nil, fmt.Errorf("%w: invalid sibling count %d (max: %d)",
			common.ErrCorruptedData, siblingCount, MaxSiblings)
	}

	siblings := make([]Sibling, 0, siblingCount)

	// Read each sibling
	for i := 0; i < int(siblingCount); i++ {
		// vclock_len (4 bytes)
		var vclockLen uint32
		if err := binary.Read(buf, binary.LittleEndian, &vclockLen); err != nil {
			return nil, fmt.Errorf("read vclock len at sibling %d: %w", i, err)
		}

		if vclockLen == 0 || vclockLen > 10000 { // Sanity check
			return nil, fmt.Errorf("%w: invalid vclock length %d", common.ErrCorruptedData, vclockLen)
		}

		// vclock bytes
		vclockBytes := make([]byte, vclockLen)
		if _, err := buf.Read(vclockBytes); err != nil {
			return nil, fmt.Errorf("read vclock at sibling %d: %w", i, err)
		}

		vclock := vclock.NewVectorClock()
		if err := vclock.UnmarshalBinary(vclockBytes); err != nil {
			return nil, fmt.Errorf("unmarshal vclock at sibling %d: %w", i, err)
		}

		// flags (1 byte) - Section 5.2
		flags, err := buf.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("read flags at sibling %d: %w", i, err)
		}

		// timestamp (8 bytes) - Section 5.1
		var timestamp int64
		if err := binary.Read(buf, binary.LittleEndian, &timestamp); err != nil {
			return nil, fmt.Errorf("read timestamp at sibling %d: %w", i, err)
		}

		// expires_at (8 bytes) — only present when FlagTTL is set
		var expiresAt int64
		if flags&FlagTTL != 0 {
			if err := binary.Read(buf, binary.LittleEndian, &expiresAt); err != nil {
				return nil, fmt.Errorf("read expires_at at sibling %d: %w", i, err)
			}
		}

		// value_len (4 bytes)
		var valueLen uint32
		if err := binary.Read(buf, binary.LittleEndian, &valueLen); err != nil {
			return nil, fmt.Errorf("read value len at sibling %d: %w", i, err)
		}

		// Validate value size (Section 3.1)
		if valueLen > MaxValueSize {
			return nil, fmt.Errorf("%w: value too large: %d bytes (max: %d)",
				common.ErrCorruptedData, valueLen, MaxValueSize)
		}

		// value bytes
		value := make([]byte, valueLen)
		if valueLen > 0 {
			if _, err := buf.Read(value); err != nil {
				return nil, fmt.Errorf("read value at sibling %d: %w", i, err)
			}
		}

		siblings = append(siblings, Sibling{
			Value:     value,
			VClock:    vclock,
			Timestamp: timestamp,
			Tombstone: (flags & FlagTombstone) != 0,
			ExpiresAt: expiresAt,
		})
	}

	return &SiblingSet{Siblings: siblings}, nil
}

// ValidateKey checks key constraints (Section 2.1)
func ValidateKey(key []byte) error {
	if len(key) == 0 {
		return fmt.Errorf("key cannot be empty")
	}
	if len(key) > MaxKeySize {
		return fmt.Errorf("key too large: %d bytes (max: %d)", len(key), MaxKeySize)
	}
	return nil
}

// ValidateValue checks value constraints (Section 3.1)
func ValidateValue(value []byte) error {
	if len(value) > MaxValueSize {
		return fmt.Errorf("value too large: %d bytes (max: %d)", len(value), MaxValueSize)
	}
	return nil
}
