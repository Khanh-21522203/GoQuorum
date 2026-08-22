package journal

import (
	"testing"
)

func TestEncodeDecodeSegmentHeader_RoundTrip(t *testing.T) {
	epoch := uint64(104)
	status := StatusWriter
	buf := EncodeSegmentHeader(epoch, status)

	if len(buf) != SegmentHeaderSize {
		t.Fatalf("len(buf) = %d, want %d", len(buf), SegmentHeaderSize)
	}

	gotEpoch, gotStatus, ok := DecodeSegmentHeader(buf)
	if !ok {
		t.Fatal("expected DecodeSegmentHeader to succeed")
	}
	if gotEpoch != epoch {
		t.Fatalf("gotEpoch = %d, want %d", gotEpoch, epoch)
	}
	if gotStatus != status {
		t.Fatalf("gotStatus = %v, want %v", gotStatus, status)
	}

	// Test StatusCompacted
	bufCompacted := EncodeSegmentHeader(105, StatusCompacted)
	ep2, st2, ok := DecodeSegmentHeader(bufCompacted)
	if !ok || ep2 != 105 || st2 != StatusCompacted {
		t.Fatalf("failed compacted header decode: ep=%d st=%v ok=%v", ep2, st2, ok)
	}
}

func TestDecodeSegmentHeader_RejectsInvalidMagicOrShortBuffer(t *testing.T) {
	// 1. Short buffer
	if _, _, ok := DecodeSegmentHeader([]byte{1, 2, 3}); ok {
		t.Fatal("expected short buffer to fail decode")
	}

	// 2. Corrupt magic
	buf := EncodeSegmentHeader(42, StatusWriter)
	buf[0] = 'X'
	if _, _, ok := DecodeSegmentHeader(buf); ok {
		t.Fatal("expected corrupted magic to fail decode")
	}
}
