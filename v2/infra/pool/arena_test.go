package pool

import (
	"bytes"
	"fmt"
	"testing"
)

func TestByteArena_SingleChunkAllocations(t *testing.T) {
	bp := NewDefaultArrayPool[byte]()
	arena := NewByteArena(bp, 1024)
	defer arena.Release()

	k1 := arena.Alloc([]byte("user:1001"))
	v1 := arena.Alloc([]byte("Alice Smith"))
	k2 := arena.AllocString("user:1002")
	v2 := arena.AllocString("Bob Jones")

	if !bytes.Equal(k1, []byte("user:1001")) {
		t.Fatalf("k1 = %q, want %q", k1, "user:1001")
	}
	if !bytes.Equal(v1, []byte("Alice Smith")) {
		t.Fatalf("v1 = %q, want %q", v1, "Alice Smith")
	}
	if !bytes.Equal(k2, []byte("user:1002")) {
		t.Fatalf("k2 = %q, want %q", k2, "user:1002")
	}
	if !bytes.Equal(v2, []byte("Bob Jones")) {
		t.Fatalf("v2 = %q, want %q", v2, "Bob Jones")
	}

	if arena.ChunkCount() != 1 {
		t.Fatalf("ChunkCount = %d, want 1", arena.ChunkCount())
	}
}

func TestByteArena_MultiChunkOverflow_PointersRemainStable(t *testing.T) {
	bp := NewDefaultArrayPool[byte]()
	// Create small 64-byte chunks to force frequent chaining
	arena := NewByteArena(bp, 64)
	defer arena.Release()

	numEntries := 20
	savedKeys := make([][]byte, numEntries)
	savedVals := make([][]byte, numEntries)

	for i := 0; i < numEntries; i++ {
		keyStr := fmt.Sprintf("key-prefix-account-%04d", i)
		valStr := fmt.Sprintf("value-payload-balance-%04d-USD", i)

		savedKeys[i] = arena.Alloc([]byte(keyStr))
		savedVals[i] = arena.Alloc([]byte(valStr))
	}

	// Verify that multiple chunks were chained
	if arena.ChunkCount() <= 1 {
		t.Fatalf("expected multiple chained chunks, got %d", arena.ChunkCount())
	}

	// Verify ALL pointers (including the earliest ones in chunk 0) are 100% STABLE and uncorrupted!
	for i := 0; i < numEntries; i++ {
		wantKey := fmt.Sprintf("key-prefix-account-%04d", i)
		wantVal := fmt.Sprintf("value-payload-balance-%04d-USD", i)

		if string(savedKeys[i]) != wantKey {
			t.Fatalf("entry %d key corrupted: got %q, want %q", i, savedKeys[i], wantKey)
		}
		if string(savedVals[i]) != wantVal {
			t.Fatalf("entry %d val corrupted: got %q, want %q", i, savedVals[i], wantVal)
		}
	}
}

func TestByteArena_ZeroAllocations(t *testing.T) {
	bp := NewDefaultArrayPool[byte]()
	arena := NewByteArena(bp, 64*1024)
	defer arena.Release()

	key := []byte("fixed-test-key")
	val := []byte("fixed-test-val")

	allocs := testing.AllocsPerRun(100, func() {
		arena.Reset()
		for i := 0; i < 50; i++ {
			_ = arena.Alloc(key)
			_ = arena.Alloc(val)
		}
	})

	if allocs != 0 {
		t.Fatalf("ByteArena Alloc allocated %f objects, want 0", allocs)
	}
}
