package journal

import (
	"errors"
	"testing"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/storage"
)

func sampleSiblingSet() *storage.SiblingSet {
	vc := vclock.NewVectorClock()
	vc.Set("node-a", 3)
	return &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte("hello"), VClock: vc, Timestamp: 100, Tombstone: false, ExpiresAt: 0},
		{Value: []byte("world"), VClock: vc.Copy(), Timestamp: 200, Tombstone: true, ExpiresAt: 999},
	}}
}

func TestEncodeDecodeRecord_RoundTrip(t *testing.T) {
	key := []byte("my-key")
	ss := sampleSiblingSet()

	data, err := EncodeRecord(key, ss)
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}

	gotKey, gotSiblings, consumed, err := DecodeRecord(data)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if consumed != len(data) {
		t.Fatalf("consumed = %d, want %d", consumed, len(data))
	}
	if string(gotKey) != string(key) {
		t.Fatalf("key mismatch: got %q, want %q", gotKey, key)
	}
	if len(gotSiblings.Siblings) != len(ss.Siblings) {
		t.Fatalf("sibling count mismatch: got %d, want %d", len(gotSiblings.Siblings), len(ss.Siblings))
	}
	for i := range ss.Siblings {
		want, got := ss.Siblings[i], gotSiblings.Siblings[i]
		if string(got.Value) != string(want.Value) || got.Timestamp != want.Timestamp ||
			got.Tombstone != want.Tombstone || got.ExpiresAt != want.ExpiresAt {
			t.Fatalf("sibling %d mismatch: got %+v, want %+v", i, got, want)
		}
		if got.VClock.Get("node-a") != want.VClock.Get("node-a") {
			t.Fatalf("sibling %d vclock mismatch: got %d, want %d", i, got.VClock.Get("node-a"), want.VClock.Get("node-a"))
		}
	}
}

func TestEncodeDecodeRecord_EmptyKeyAndEmptySiblings(t *testing.T) {
	data, err := EncodeRecord(nil, &storage.SiblingSet{})
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	key, siblings, consumed, err := DecodeRecord(data)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if len(key) != 0 {
		t.Fatalf("expected empty key, got %q", key)
	}
	if len(siblings.Siblings) != 0 {
		t.Fatalf("expected 0 siblings, got %d", len(siblings.Siblings))
	}
	if consumed != len(data) {
		t.Fatalf("consumed = %d, want %d", consumed, len(data))
	}
}

func TestDecodeRecord_NeverPanicsOnTruncatedInput(t *testing.T) {
	data, err := EncodeRecord([]byte("k"), sampleSiblingSet())
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
	data, err := EncodeRecord([]byte("k"), sampleSiblingSet())
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	// Flip a bit inside the key/sibling payload (the CRC32 field's own
	// bytes are untouched) so DecodeRecord gets far enough to compare
	// checksums instead of failing an earlier bounds check.
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] ^= 0xFF

	_, _, _, err = DecodeRecord(corrupt)
	if !errors.Is(err, quorumerr.ErrCorruptedData) {
		t.Fatalf("expected ErrCorruptedData, got %v", err)
	}
}

func TestEncodeRecord_RejectsOversizedKey(t *testing.T) {
	bigKey := make([]byte, 1<<16) // one past math.MaxUint16
	if _, err := EncodeRecord(bigKey, &storage.SiblingSet{}); err == nil {
		t.Fatal("expected an error encoding an oversized key")
	}
}
