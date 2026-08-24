package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
		data, err := EncodeRecord(key, []byte("val-"+string(key)))
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

	firstData, err := EncodeRecord([]byte("k"), []byte("first-val"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	if _, err := f.Write(firstData); err != nil {
		t.Fatalf("Write: %v", err)
	}

	secondData, err := EncodeRecord([]byte("k"), []byte("second-val"))
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
}

func TestReplay_TruncatedTailRecoveredUpToLastValidRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.log")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	dataA, err := EncodeRecord([]byte("a"), []byte("val-a"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	dataB, err := EncodeRecord([]byte("b"), []byte("val-b"))
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

	// Simulate a crash mid-write: only half of the second record actually made it to disk.
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

func TestReplayRingSegments_StatusCompactedAnchorSkipsOlderSegments(t *testing.T) {
	dir := t.TempDir()
	files := make([]*os.File, 4)
	for i := 0; i < 4; i++ {
		p := filepath.Join(dir, fmt.Sprintf("wal_%d.log", i))
		f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		defer f.Close()
		files[i] = f
	}

	// 1. Seg 0: Stale writer from ancient Epoch 100 with key "ancient-key"
	hdr0 := EncodeSegmentHeader(100, StatusWriter)
	_, _ = files[0].WriteAt(hdr0, 0)
	rec0, _ := EncodeRecord([]byte("ancient-key"), []byte("ancient-val"))
	_, _ = files[0].WriteAt(rec0, SegmentHeaderSize)

	// 2. Seg 1: StatusCompacted checkpoint at Epoch 102 with key "base-key"
	hdr1 := EncodeSegmentHeader(102, StatusCompacted)
	_, _ = files[1].WriteAt(hdr1, 0)
	rec1, _ := EncodeRecord([]byte("base-key"), []byte("base-val"))
	_, _ = files[1].WriteAt(rec1, SegmentHeaderSize)

	// 3. Seg 2: StatusWriter at Epoch 103 with key "writer-key"
	hdr2 := EncodeSegmentHeader(103, StatusWriter)
	_, _ = files[2].WriteAt(hdr2, 0)
	rec2, _ := EncodeRecord([]byte("writer-key"), []byte("writer-val"))
	_, _ = files[2].WriteAt(rec2, SegmentHeaderSize)

	// 4. Seg 3: StatusEmpty / uninitialized
	hdr3 := EncodeSegmentHeader(0, StatusEmpty)
	_, _ = files[3].WriteAt(hdr3, 0)

	// Run ReplayRingSegments
	idx, activeSeg, tailSeg, maxEpoch, tailOffset, err := ReplayRingSegments(files)
	if err != nil {
		t.Fatalf("ReplayRingSegments: %v", err)
	}

	// Check activeSeg is Seg 2 (latest writer)
	if activeSeg != 2 {
		t.Fatalf("activeSeg = %d, want 2", activeSeg)
	}
	if tailSeg != 1 {
		t.Fatalf("tailSeg = %d, want 1 (compacted base)", tailSeg)
	}
	if maxEpoch != 103 {
		t.Fatalf("maxEpoch = %d, want 103", maxEpoch)
	}
	if tailOffset != SegmentHeaderSize+int64(len(rec2)) {
		t.Fatalf("tailOffset = %d, want %d", tailOffset, SegmentHeaderSize+int64(len(rec2)))
	}

	// Verify "base-key" from compacted base exists
	if _, ok := idx.Get([]byte("base-key")); !ok {
		t.Fatal("expected base-key from compacted segment to be present in index")
	}

	// Verify "writer-key" from active writer exists
	if _, ok := idx.Get([]byte("writer-key")); !ok {
		t.Fatal("expected writer-key to be present in index")
	}

	// Verify "ancient-key" from Epoch 100 was SKIPPED completely!
	if _, ok := idx.Get([]byte("ancient-key")); ok {
		t.Fatal("expected ancient-key before compaction checkpoint to be SKIPPED")
	}
}
