package iouring

import (
	"bytes"
	"testing"
)

func TestReassembler_SingleFrameInOneFeed(t *testing.T) {
	const testMsgID uint16 = 10
	frame := EncodeFrame(testMsgID, 1, 100, []byte("payload"))

	var r Reassembler
	r.Feed(frame)

	hdr, body, ok := r.Next()
	if !ok {
		t.Fatal("Next() = false, want true")
	}
	if hdr.CorrelationID != 100 || hdr.MessageID != testMsgID {
		t.Errorf("unexpected header: %+v", hdr)
	}
	if !bytes.Equal(body, []byte("payload")) {
		t.Errorf("body = %q, want %q", body, "payload")
	}

	if _, _, ok := r.Next(); ok {
		t.Fatal("Next() after single frame consumed = true, want false")
	}
}

func TestReassembler_TwoFramesConcatenatedInOneFeed(t *testing.T) {
	const msg1 uint16 = 1
	const msg2 uint16 = 2
	frame1 := EncodeFrame(msg1, 1, 1, []byte("first"))
	frame2 := EncodeFrame(msg2, 1, 2, []byte("second"))

	var r Reassembler
	r.Feed(append(append([]byte{}, frame1...), frame2...))

	hdr1, body1, ok := r.Next()
	if !ok {
		t.Fatal("Next() for frame 1 = false, want true")
	}
	if hdr1.CorrelationID != 1 || hdr1.MessageID != msg1 || !bytes.Equal(body1, []byte("first")) {
		t.Errorf("frame 1 mismatch: hdr=%+v body=%q", hdr1, body1)
	}

	hdr2, body2, ok := r.Next()
	if !ok {
		t.Fatal("Next() for frame 2 = false, want true")
	}
	if hdr2.CorrelationID != 2 || hdr2.MessageID != msg2 || !bytes.Equal(body2, []byte("second")) {
		t.Errorf("frame 2 mismatch: hdr=%+v body=%q", hdr2, body2)
	}

	if _, _, ok := r.Next(); ok {
		t.Fatal("Next() after both frames consumed = true, want false")
	}
}

func TestReassembler_FrameSplitAcrossFeeds(t *testing.T) {
	const testMsgID uint16 = 55
	frame := EncodeFrame(testMsgID, 1, 55, []byte("root-bytes-payload"))

	// Split at every possible offset, including inside the 16-byte header.
	for split := 0; split <= len(frame); split++ {
		t.Run("", func(t *testing.T) {
			var r Reassembler
			r.Feed(frame[:split])

			if split < len(frame) {
				if _, _, ok := r.Next(); ok {
					t.Fatalf("split=%d: Next() before full frame fed = true, want false", split)
				}
			}

			r.Feed(frame[split:])
			hdr, body, ok := r.Next()
			if !ok {
				t.Fatalf("split=%d: Next() after full frame fed = false, want true", split)
			}
			if hdr.CorrelationID != 55 || hdr.MessageID != testMsgID {
				t.Fatalf("split=%d: unexpected header %+v", split, hdr)
			}
			if !bytes.Equal(body, []byte("root-bytes-payload")) {
				t.Fatalf("split=%d: body = %q, want %q", split, body, "root-bytes-payload")
			}
		})
	}
}

func TestReassembler_PartialNextFrameRetained(t *testing.T) {
	frame1 := EncodeFrame(1, 1, 1, []byte("complete"))
	frame2 := EncodeFrame(2, 1, 2, []byte("also-complete"))

	var r Reassembler
	// Feed frame1 plus only the first half of frame2.
	partial := frame2[:len(frame2)/2]
	r.Feed(append(append([]byte{}, frame1...), partial...))

	hdr1, body1, ok := r.Next()
	if !ok {
		t.Fatal("Next() for frame 1 = false, want true")
	}
	if hdr1.CorrelationID != 1 || !bytes.Equal(body1, []byte("complete")) {
		t.Errorf("frame 1 mismatch: hdr=%+v body=%q", hdr1, body1)
	}

	// Only the partial second frame remains; not enough to decode.
	if _, _, ok := r.Next(); ok {
		t.Fatal("Next() on partial frame 2 = true, want false")
	}

	// Feed the rest of frame 2.
	r.Feed(frame2[len(frame2)/2:])
	hdr2, body2, ok := r.Next()
	if !ok {
		t.Fatal("Next() for frame 2 after completing feed = false, want true")
	}
	if hdr2.CorrelationID != 2 || !bytes.Equal(body2, []byte("also-complete")) {
		t.Errorf("frame 2 mismatch: hdr=%+v body=%q", hdr2, body2)
	}
}

func TestReassembler_ManyFramesThenPartial(t *testing.T) {
	frame1 := EncodeFrame(1, 1, 1, []byte("a"))
	frame2 := EncodeFrame(1, 1, 2, []byte("bb"))
	frame3 := EncodeFrame(1, 1, 3, []byte("ccc"))

	var buf []byte
	buf = append(buf, frame1...)
	buf = append(buf, frame2...)
	buf = append(buf, frame3[:5]...) // partial third frame

	var r Reassembler
	r.Feed(buf)

	var gotIDs []uint64
	for {
		hdr, _, ok := r.Next()
		if !ok {
			break
		}
		gotIDs = append(gotIDs, hdr.CorrelationID)
	}
	if len(gotIDs) != 2 || gotIDs[0] != 1 || gotIDs[1] != 2 {
		t.Fatalf("got correlation IDs %v, want [1 2]", gotIDs)
	}

	r.Feed(frame3[5:])
	hdr3, body3, ok := r.Next()
	if !ok {
		t.Fatal("Next() for frame 3 after completing feed = false, want true")
	}
	if hdr3.CorrelationID != 3 || !bytes.Equal(body3, []byte("ccc")) {
		t.Errorf("frame 3 mismatch: hdr=%+v body=%q", hdr3, body3)
	}
}

func TestReassembler_EmptyFeedNeverPanics(t *testing.T) {
	var r Reassembler
	r.Feed(nil)
	if _, _, ok := r.Next(); ok {
		t.Fatal("Next() on empty reassembler = true, want false")
	}
}

func BenchmarkReassembler_FeedNext(b *testing.B) {
	frame := EncodeFrame(1, 1, 12345, []byte("benchmark-payload-data"))
	r := NewReassembler(nil, DefaultReassemblerCap)
	defer r.Release()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Feed(frame)
		_, _, _ = r.Next()
	}
}
