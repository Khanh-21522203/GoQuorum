package iouring

import (
	"bytes"
	"errors"
	"testing"

	"goquorum.io/v2/contracts/quorumerr"
)

func TestEncodeFrame_DecodeFrameHeader_RoundTrip(t *testing.T) {
	body := []byte("hello, frame")
	frame := EncodeFrame(uint16(MsgHeartbeatRequest), 3, 42, body)

	if len(frame) != FrameHeaderSize+len(body) {
		t.Fatalf("frame length = %d, want %d", len(frame), FrameHeaderSize+len(body))
	}

	hdr, err := DecodeFrameHeader(frame)
	if err != nil {
		t.Fatalf("DecodeFrameHeader: %v", err)
	}
	if hdr.TotalLength != uint32(FrameHeaderSize+len(body)) {
		t.Errorf("TotalLength = %d, want %d", hdr.TotalLength, FrameHeaderSize+len(body))
	}
	if hdr.MessageID != MsgHeartbeatRequest {
		t.Errorf("MessageID = %v, want %v", hdr.MessageID, MsgHeartbeatRequest)
	}
	if hdr.SchemaVersion != 3 {
		t.Errorf("SchemaVersion = %d, want 3", hdr.SchemaVersion)
	}
	if hdr.CorrelationID != 42 {
		t.Errorf("CorrelationID = %d, want 42", hdr.CorrelationID)
	}
	if !bytes.Equal(frame[FrameHeaderSize:], body) {
		t.Errorf("frame body = %q, want %q", frame[FrameHeaderSize:], body)
	}
}

func TestEncodeFrame_EmptyBody(t *testing.T) {
	frame := EncodeFrame(uint16(MsgHeartbeatRequest), 1, 7, nil)
	if len(frame) != FrameHeaderSize {
		t.Fatalf("frame length = %d, want %d", len(frame), FrameHeaderSize)
	}
	hdr, err := DecodeFrameHeader(frame)
	if err != nil {
		t.Fatalf("DecodeFrameHeader: %v", err)
	}
	if hdr.TotalLength != FrameHeaderSize {
		t.Errorf("TotalLength = %d, want %d", hdr.TotalLength, FrameHeaderSize)
	}
}

func TestDecodeFrameHeader_TooShort(t *testing.T) {
	for n := 0; n < FrameHeaderSize; n++ {
		data := make([]byte, n)
		_, err := DecodeFrameHeader(data)
		if err == nil {
			t.Fatalf("DecodeFrameHeader(%d bytes): expected error, got nil", n)
		}
		if !errors.Is(err, quorumerr.ErrCorruptedData) {
			t.Errorf("DecodeFrameHeader(%d bytes): error = %v, want wrapping ErrCorruptedData", n, err)
		}
	}
}

func TestReassembler_SingleFrameInOneFeed(t *testing.T) {
	frame := EncodeFrame(uint16(MsgHeartbeatRequest), 1, 100, []byte("payload"))

	var r Reassembler
	r.Feed(frame)

	hdr, body, ok := r.Next()
	if !ok {
		t.Fatal("Next() = false, want true")
	}
	if hdr.CorrelationID != 100 || hdr.MessageID != MsgHeartbeatRequest {
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
	frame1 := EncodeFrame(uint16(MsgHeartbeatRequest), 1, 1, []byte("first"))
	frame2 := EncodeFrame(uint16(MsgHeartbeatResponse), 1, 2, []byte("second"))

	var r Reassembler
	r.Feed(append(append([]byte{}, frame1...), frame2...))

	hdr1, body1, ok := r.Next()
	if !ok {
		t.Fatal("Next() for frame 1 = false, want true")
	}
	if hdr1.CorrelationID != 1 || !bytes.Equal(body1, []byte("first")) {
		t.Errorf("frame 1 mismatch: hdr=%+v body=%q", hdr1, body1)
	}

	hdr2, body2, ok := r.Next()
	if !ok {
		t.Fatal("Next() for frame 2 = false, want true")
	}
	if hdr2.CorrelationID != 2 || !bytes.Equal(body2, []byte("second")) {
		t.Errorf("frame 2 mismatch: hdr=%+v body=%q", hdr2, body2)
	}

	if _, _, ok := r.Next(); ok {
		t.Fatal("Next() after both frames consumed = true, want false")
	}
}

func TestReassembler_FrameSplitAcrossFeeds(t *testing.T) {
	frame := EncodeFrame(uint16(MsgGetMerkleRootResponse), 1, 55, []byte("root-bytes-payload"))

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
			if hdr.CorrelationID != 55 || hdr.MessageID != MsgGetMerkleRootResponse {
				t.Fatalf("split=%d: unexpected header %+v", split, hdr)
			}
			if !bytes.Equal(body, []byte("root-bytes-payload")) {
				t.Fatalf("split=%d: body = %q, want %q", split, body, "root-bytes-payload")
			}
		})
	}
}

func TestReassembler_PartialNextFrameRetained(t *testing.T) {
	frame1 := EncodeFrame(uint16(MsgHeartbeatRequest), 1, 1, []byte("complete"))
	frame2 := EncodeFrame(uint16(MsgHeartbeatResponse), 1, 2, []byte("also-complete"))

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
	frame1 := EncodeFrame(uint16(MsgHeartbeatRequest), 1, 1, []byte("a"))
	frame2 := EncodeFrame(uint16(MsgHeartbeatRequest), 1, 2, []byte("bb"))
	frame3 := EncodeFrame(uint16(MsgHeartbeatRequest), 1, 3, []byte("ccc"))

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
