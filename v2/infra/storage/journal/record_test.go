package journal

import (
	"bytes"
	"errors"
	"testing"

	"goquorum.io/v2/contracts/quorumerr"
)

func TestEncodeDecodeRecord_RoundTrip(t *testing.T) {
	key := []byte("my-key")
	val := []byte("my-value-payload-12345")

	data, err := EncodeRecord(key, val)
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}

	gotKey, gotVal, consumed, err := DecodeRecord(data)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if consumed != len(data) {
		t.Fatalf("consumed = %d, want %d", consumed, len(data))
	}
	if !bytes.Equal(gotKey, key) {
		t.Fatalf("key mismatch: got %q, want %q", gotKey, key)
	}
	if !bytes.Equal(gotVal, val) {
		t.Fatalf("val mismatch: got %q, want %q", gotVal, val)
	}
}

func TestEncodeDecodeRecord_EmptyKeyAndEmptyVal(t *testing.T) {
	data, err := EncodeRecord(nil, nil)
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	key, val, consumed, err := DecodeRecord(data)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if len(key) != 0 {
		t.Fatalf("expected empty key, got %q", key)
	}
	if len(val) != 0 {
		t.Fatalf("expected empty val, got %q", val)
	}
	if consumed != len(data) {
		t.Fatalf("consumed = %d, want %d", consumed, len(data))
	}
}

func TestDecodeRecord_NeverPanicsOnTruncatedInput(t *testing.T) {
	data, err := EncodeRecord([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	for n := 0; n < len(data); n++ {
		_, _, _, err := DecodeRecord(data[:n])
		if err == nil {
			t.Fatalf("expected error decoding %d/%d truncated bytes, got nil", n, len(data))
		}
		if !errors.Is(err, quorumerr.ErrCorruptedData) {
			t.Fatalf("expected ErrCorruptedData at %d/%d bytes, got %v", n, len(data), err)
		}
	}
}

func TestDecodeRecord_DetectsChecksumCorruption(t *testing.T) {
	data, err := EncodeRecord([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] ^= 0xFF

	_, _, _, err = DecodeRecord(corrupt)
	if !errors.Is(err, quorumerr.ErrCorruptedData) {
		t.Fatalf("expected ErrCorruptedData, got %v", err)
	}
}

func TestEncodeRecord_RejectsOversizedKey(t *testing.T) {
	bigKey := make([]byte, 1<<16) // one past math.MaxUint16
	if _, err := EncodeRecord(bigKey, []byte("v")); err == nil {
		t.Fatal("expected an error encoding an oversized key")
	}
}
