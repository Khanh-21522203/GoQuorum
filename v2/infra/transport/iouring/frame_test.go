package iouring

import (
	"bytes"
	"errors"
	"testing"

	"goquorum.io/v2/contracts/quorumerr"
)

// EncodeFrame is a test helper that serializes a frame into a newly allocated slice.
func EncodeFrame(msgID uint16, schemaVersion uint16, correlationID uint64, body []byte) []byte {
	return EncodeFrameTo(nil, msgID, schemaVersion, correlationID, body)
}

func TestDecodeFrame_RoundTrip(t *testing.T) {
	body := []byte("hello, frame payload")
	const testMsgID uint16 = 5
	frame := EncodeFrame(testMsgID, 3, 42, body)

	if len(frame) != FrameHeaderSize+len(body) {
		t.Fatalf("frame length = %d, want %d", len(frame), FrameHeaderSize+len(body))
	}

	hdr, decodedBody, consumed, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if consumed != len(frame) {
		t.Errorf("consumed = %d, want %d", consumed, len(frame))
	}
	if hdr.TotalLength != uint32(FrameHeaderSize+len(body)) {
		t.Errorf("TotalLength = %d, want %d", hdr.TotalLength, FrameHeaderSize+len(body))
	}
	if hdr.MessageID != testMsgID {
		t.Errorf("MessageID = %d, want %d", hdr.MessageID, testMsgID)
	}
	if hdr.SchemaVersion != 3 {
		t.Errorf("SchemaVersion = %d, want 3", hdr.SchemaVersion)
	}
	if hdr.CorrelationID != 42 {
		t.Errorf("CorrelationID = %d, want 42", hdr.CorrelationID)
	}
	if !bytes.Equal(decodedBody, body) {
		t.Errorf("frame body = %q, want %q", decodedBody, body)
	}
}

func TestDecodeFrame_EmptyBody(t *testing.T) {
	const testMsgID uint16 = 5
	frame := EncodeFrame(testMsgID, 1, 7, nil)
	if len(frame) != FrameHeaderSize {
		t.Fatalf("frame length = %d, want %d", len(frame), FrameHeaderSize)
	}
	hdr, decodedBody, consumed, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if consumed != FrameHeaderSize || hdr.TotalLength != FrameHeaderSize {
		t.Errorf("consumed = %d, TotalLength = %d, want %d", consumed, hdr.TotalLength, FrameHeaderSize)
	}
	if len(decodedBody) != 0 {
		t.Errorf("decodedBody = %q, want empty", decodedBody)
	}
}

func TestDecodeFrame_TooShort(t *testing.T) {
	for n := 0; n < FrameHeaderSize; n++ {
		data := make([]byte, n)
		_, _, _, err := DecodeFrame(data)
		if err == nil {
			t.Fatalf("DecodeFrame(%d bytes): expected error, got nil", n)
		}
		if !errors.Is(err, quorumerr.ErrCorruptedData) {
			t.Errorf("DecodeFrame(%d bytes): error = %v, want wrapping ErrCorruptedData", n, err)
		}
	}
}
