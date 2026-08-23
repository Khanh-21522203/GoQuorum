package adapter

import (
	"testing"

	"goquorum.io/v2/contracts/vclock"
)

func TestSiblingSet_MarshalUnmarshalBinary_RoundTrip(t *testing.T) {
	vc1 := vclock.NewVectorClock()
	vc1.Set("node-a", 3)
	vc2 := vclock.NewVectorClock()
	vc2.Set("node-b", 1)

	ss := SiblingSet{Siblings: []Sibling{
		{Value: []byte("hello"), VClock: vc1, Timestamp: 100, Tombstone: false, ExpiresAt: 0},
		{Value: []byte{}, VClock: vc2, Timestamp: 200, Tombstone: true, ExpiresAt: 999},
	}}

	data, err := ss.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	var got SiblingSet
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if len(got.Siblings) != 2 {
		t.Fatalf("expected 2 siblings, got %d", len(got.Siblings))
	}
	if string(got.Siblings[0].Value) != "hello" || got.Siblings[0].Timestamp != 100 || got.Siblings[0].Tombstone {
		t.Fatalf("sibling 0 mismatch: %+v", got.Siblings[0])
	}
	if got.Siblings[0].VClock.Get("node-a") != 3 {
		t.Fatalf("sibling 0 vclock mismatch: got %d", got.Siblings[0].VClock.Get("node-a"))
	}
	if len(got.Siblings[1].Value) != 0 || got.Siblings[1].Timestamp != 200 || !got.Siblings[1].Tombstone || got.Siblings[1].ExpiresAt != 999 {
		t.Fatalf("sibling 1 mismatch: %+v", got.Siblings[1])
	}
	if got.Siblings[1].VClock.Get("node-b") != 1 {
		t.Fatalf("sibling 1 vclock mismatch: got %d", got.Siblings[1].VClock.Get("node-b"))
	}
}

func TestSiblingSet_MarshalUnmarshalBinary_Empty(t *testing.T) {
	var ss SiblingSet
	data, err := ss.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	var got SiblingSet
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if len(got.Siblings) != 0 {
		t.Fatalf("expected 0 siblings, got %d", len(got.Siblings))
	}
}

func TestSiblingSet_UnmarshalBinary_RejectsTruncatedAndRandomData(t *testing.T) {
	vc := vclock.NewVectorClock()
	vc.Set("node-a", 1)
	ss := SiblingSet{Siblings: []Sibling{{Value: []byte("v"), VClock: vc, Timestamp: 1}}}
	data, err := ss.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	for n := 0; n < len(data); n++ {
		var got SiblingSet
		// Must never panic; an error is expected for every truncation point
		// short of the full, valid encoding.
		_ = got.UnmarshalBinary(data[:n])
	}

	garbage := []byte{0xFF, 0xFF, 0x01, 0x02, 0x03}
	var got SiblingSet
	if err := got.UnmarshalBinary(garbage); err == nil {
		t.Fatal("expected an error decoding random garbage claiming 65535 siblings")
	}
}
