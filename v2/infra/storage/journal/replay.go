package journal

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
)

// Replay reads the WAL file from the beginning, reconstructs the in-memory
// index, and returns the write tail offset. Replay stops without error at
// the first corrupted or truncated record (treating torn writes at crash time
// as the end of the log).
func Replay(f *os.File) (idx *index, tailOffset int64, err error) {
	idx = newIndex()
	r := bufio.NewReader(f)

	var offset int64
	lengthBuf := make([]byte, recordLengthFieldSize)
	for {
		n, readErr := io.ReadFull(r, lengthBuf)
		if n == 0 && readErr == io.EOF {
			break // Clean end of log
		}
		if readErr != nil {
			break // Truncated record length at EOF
		}

		length := binary.BigEndian.Uint32(lengthBuf)
		body := make([]byte, length)
		if _, readErr := io.ReadFull(r, body); readErr != nil {
			break // Truncated body at EOF
		}

		full := make([]byte, 0, recordLengthFieldSize+len(body))
		full = append(full, lengthBuf...)
		full = append(full, body...)

		key, _, consumed, decErr := DecodeRecord(full)
		if decErr != nil {
			break // Checksum mismatch or corruption; stop replay here
		}

		idx.Set(key, indexEntry{
			Offset: offset,
			Length: uint32(consumed),
		})
		offset += int64(consumed)
	}

	return idx, offset, nil
}
