package journal

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
)

// Replay sequentially reads every record from the start of f, rebuilding
// the in-memory index a Store needs to serve reads, and returns the byte
// offset immediately after the last fully valid record (where the next
// write should land).
//
// This is the one deliberate synchronous, blocking I/O path in this
// package: it runs once, at startup, before any reactor is running to
// route calls through, so there is no concurrency to protect and nothing
// else contending for the file.
//
// If a record fails to decode — because it was torn by a crash mid-write,
// or its declared Length reaches past the current end of file — Replay
// stops there without returning an error: everything read before that
// point is treated as valid (last write per key wins, consistent with
// append-only, sequential replay order), and tailOffset marks the end of
// that valid prefix. This is standard write-ahead-log convention: a
// truncated *final* record is an expected consequence of a crash between
// "write" and "fsync", not data corruption to fail startup over.
func Replay(f *os.File) (idx *index, tailOffset int64, err error) {
	idx = newIndex()
	r := bufio.NewReader(f)

	var offset int64
	lengthBuf := make([]byte, recordLengthFieldSize)
	for {
		n, readErr := io.ReadFull(r, lengthBuf)
		if n == 0 && readErr == io.EOF {
			// Clean end of log, exactly on a record boundary.
			break
		}
		if readErr != nil {
			// A partial length header: the last write was torn before it
			// even got this far. Discard it and stop.
			break
		}

		length := binary.BigEndian.Uint32(lengthBuf)
		body := make([]byte, length)
		if _, readErr := io.ReadFull(r, body); readErr != nil {
			// The declared record length reaches past EOF: a torn write.
			// Discard it and stop.
			break
		}

		full := make([]byte, 0, recordLengthFieldSize+len(body))
		full = append(full, lengthBuf...)
		full = append(full, body...)

		key, siblings, consumed, decErr := DecodeRecord(full)
		if decErr != nil {
			// Checksum mismatch or malformed body: treat the same as a
			// torn write and stop, keeping everything decoded so far.
			break
		}

		idx.Set(key, indexEntry{
			Offset:    offset,
			Length:    uint32(consumed),
			Tombstone: lastSiblingIsTombstone(siblings),
		})
		offset += int64(consumed)
	}

	return idx, offset, nil
}
