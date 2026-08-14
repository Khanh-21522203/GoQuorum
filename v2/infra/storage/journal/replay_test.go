package journal

import (
	"os"
	"path/filepath"
	"testing"

	"goquorum.io/v2/engine/storage"
)

func TestReplay_EmptyFile(t *testing.T) {
	f := mustOpenTempFile(t)
	defer f.Close()

	idx, tail, err := Replay(f)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if tail != 0 {
		t.Fatalf("tail = %d, want 0", tail)
	}
	if idx.Len() != 0 {
		t.Fatalf("index len = %d, want 0", idx.Len())
	}
}

func TestReplay_RebuildsIndexFromValidRecords(t *testing.T) {
	f := mustOpenTempFile(t)
	defer f.Close()

	type written struct {
		key    []byte
		offset int64
	}
	var entries []written
	var offset int64
	for _, key := range [][]byte{[]byte("a"), []byte("b"), []byte("c")} {
		data, err := EncodeRecord(key, sampleSiblingSet())
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatalf("Write: %v", err)
		}
		entries = append(entries, written{key: key, offset: offset})
		offset += int64(len(data))
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	idx, tail, err := Replay(f)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if tail != offset {
		t.Fatalf("tail = %d, want %d", tail, offset)
	}
	if idx.Len() != len(entries) {
		t.Fatalf("index len = %d, want %d", idx.Len(), len(entries))
	}
	for _, e := range entries {
		got, ok := idx.Get(e.key)
		if !ok {
			t.Fatalf("missing key %q", e.key)
		}
		if got.Offset != e.offset {
			t.Fatalf("offset mismatch for %q: got %d, want %d", e.key, got.Offset, e.offset)
		}
	}
}

func TestReplay_LastWritePerKeyWins(t *testing.T) {
	f := mustOpenTempFile(t)
	defer f.Close()

	firstData, err := EncodeRecord([]byte("k"), sampleSiblingSet())
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	if _, err := f.Write(firstData); err != nil {
		t.Fatalf("Write: %v", err)
	}

	tombstoneSet := &storage.SiblingSet{Siblings: []storage.Sibling{{Tombstone: true, Timestamp: 300}}}
	secondData, err := EncodeRecord([]byte("k"), tombstoneSet)
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	if _, err := f.Write(secondData); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	idx, tail, err := Replay(f)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if tail != int64(len(firstData)+len(secondData)) {
		t.Fatalf("tail = %d, want %d", tail, len(firstData)+len(secondData))
	}
	got, ok := idx.Get([]byte("k"))
	if !ok {
		t.Fatal("expected key k to be present")
	}
	if got.Offset != int64(len(firstData)) {
		t.Fatalf("expected the index to point at the second (last) write, got offset %d, want %d", got.Offset, len(firstData))
	}
	if !got.Tombstone {
		t.Fatal("expected the last write's tombstone flag to win")
	}
}

func TestReplay_TruncatedTailRecoveredUpToLastValidRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.log")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	dataA, err := EncodeRecord([]byte("a"), sampleSiblingSet())
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	dataB, err := EncodeRecord([]byte("b"), sampleSiblingSet())
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	if _, err := f.Write(dataA); err != nil {
		t.Fatalf("Write: %v", err)
	}
	validTail := int64(len(dataA))
	if _, err := f.Write(dataB); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Simulate a crash mid-write: only half of the second record actually
	// made it to disk.
	if err := f.Truncate(validTail + int64(len(dataB)/2)); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f2, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile (reopen): %v", err)
	}
	defer f2.Close()

	idx, tail, err := Replay(f2)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if tail != validTail {
		t.Fatalf("tail = %d, want %d", tail, validTail)
	}
	if idx.Len() != 1 {
		t.Fatalf("index len = %d, want 1", idx.Len())
	}
	if _, ok := idx.Get([]byte("a")); !ok {
		t.Fatal("expected key a (fully written) to survive")
	}
	if _, ok := idx.Get([]byte("b")); ok {
		t.Fatal("expected key b (torn write) to be discarded")
	}
}

func mustOpenTempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "wal.log"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	return f
}
