package vclock

import (
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
)

func TestTick_IncrementsFromZero(t *testing.T) {
	vc := NewVectorClock()
	vc.Tick("a")
	if got := vc.Get("a"); got != 1 {
		t.Fatalf("expected counter 1, got %d", got)
	}
	vc.Tick("a")
	if got := vc.Get("a"); got != 2 {
		t.Fatalf("expected counter 2, got %d", got)
	}
}

func TestTick_OnZeroValueVectorClock(t *testing.T) {
	var vc VectorClock
	vc.Tick("a")
	if got := vc.Get("a"); got != 1 {
		t.Fatalf("expected counter 1 on zero-value VectorClock, got %d", got)
	}
}

func TestSet_OverwritesCounter(t *testing.T) {
	vc := NewVectorClock()
	vc.Set("a", 5)
	if got := vc.Get("a"); got != 5 {
		t.Fatalf("expected counter 5, got %d", got)
	}
}

func TestGet_AbsentNodeReturnsZero(t *testing.T) {
	vc := NewVectorClock()
	if got := vc.Get("missing"); got != 0 {
		t.Fatalf("expected 0 for absent node, got %d", got)
	}
}

func TestCopy_IsIndependent(t *testing.T) {
	vc1 := NewVectorClock()
	vc1.Tick("a")
	vc2 := vc1.Copy()
	vc2.Tick("a")

	if got := vc1.Get("a"); got != 1 {
		t.Fatalf("mutating the copy affected the original: got %d, want 1", got)
	}
	if got := vc2.Get("a"); got != 2 {
		t.Fatalf("expected copy's counter to be 2, got %d", got)
	}
}

func TestPlainAssignment_AliasesUnderlyingMap(t *testing.T) {
	vc1 := NewVectorClock()
	vc1.Tick("a")
	vc2 := vc1 // documented footgun: shallow copy, shares the map
	vc2.Tick("a")

	if got := vc1.Get("a"); got != 2 {
		t.Fatalf("expected plain assignment to alias the map (got %d), Copy() is required for isolation", got)
	}
}

func TestMerge_TakesMaxCounter(t *testing.T) {
	vc1 := NewVectorClock()
	vc1.Set("a", 3)
	vc2 := NewVectorClock()
	vc2.Set("a", 5)
	vc2.Set("b", 1)

	vc1.Merge(vc2)

	if got := vc1.Get("a"); got != 5 {
		t.Fatalf("expected max(3,5)=5, got %d", got)
	}
	if got := vc1.Get("b"); got != 1 {
		t.Fatalf("expected new entry b=1 to be added, got %d", got)
	}
}

func TestMerge_OnZeroValueVectorClock(t *testing.T) {
	var vc VectorClock
	other := NewVectorClock()
	other.Set("a", 1)
	vc.Merge(other)
	if got := vc.Get("a"); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b func() VectorClock
		want Ordering
	}{
		{
			name: "equal empty clocks",
			a:    NewVectorClock,
			b:    NewVectorClock,
			want: Equal,
		},
		{
			name: "equal identical entries",
			a:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 1); return vc },
			b:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 1); return vc },
			want: Equal,
		},
		{
			name: "before: a is a strict subset",
			a:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 1); return vc },
			b:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 1); vc.Set("b", 1); return vc },
			want: Before,
		},
		{
			name: "after: b is a strict subset",
			a:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 1); vc.Set("b", 1); return vc },
			b:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 1); return vc },
			want: After,
		},
		{
			name: "concurrent: divergent updates",
			a:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 2); vc.Set("b", 1); return vc },
			b:    func() VectorClock { vc := NewVectorClock(); vc.Set("a", 1); vc.Set("b", 2); return vc },
			want: Concurrent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := tt.a(), tt.b()
			if got := a.Compare(b); got != tt.want {
				t.Fatalf("Compare() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHappensBeforeAfterConcurrentEquals(t *testing.T) {
	before := NewVectorClock()
	before.Set("a", 1)
	after := NewVectorClock()
	after.Set("a", 1)
	after.Set("b", 1)

	if !before.HappensBefore(after) {
		t.Fatal("expected before.HappensBefore(after)")
	}
	if !after.HappensAfter(before) {
		t.Fatal("expected after.HappensAfter(before)")
	}
	if !after.Dominates(before) {
		t.Fatal("expected after.Dominates(before)")
	}
	if before.Dominates(after) {
		t.Fatal("did not expect before.Dominates(after)")
	}
	if !before.Equals(before.Copy()) {
		t.Fatal("expected a clock to equal its own copy")
	}

	concA := NewVectorClock()
	concA.Set("a", 2)
	concB := NewVectorClock()
	concB.Set("b", 2)
	if !concA.IsConcurrentWith(concB) {
		t.Fatal("expected concA.IsConcurrentWith(concB)")
	}
}

func TestIsEmptyAndSize(t *testing.T) {
	vc := NewVectorClock()
	if !vc.IsEmpty() || vc.Size() != 0 {
		t.Fatalf("expected empty/size 0, got IsEmpty=%v Size=%d", vc.IsEmpty(), vc.Size())
	}
	vc.Tick("a")
	if vc.IsEmpty() || vc.Size() != 1 {
		t.Fatalf("expected non-empty/size 1, got IsEmpty=%v Size=%d", vc.IsEmpty(), vc.Size())
	}
}

func TestPrune_RemovesOldEntries(t *testing.T) {
	vc := NewVectorClock()
	vc.Set("old", 1)
	vc.entries["old"].timestamp = time.Now().Add(-time.Hour).Unix()
	vc.Set("fresh", 1)

	removed := vc.Prune(time.Minute, 0)
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}
	if vc.Get("old") != 0 {
		t.Fatal("expected old entry to be pruned")
	}
	if vc.Get("fresh") != 1 {
		t.Fatal("expected fresh entry to survive")
	}
}

func TestPrune_TrimsToMaxEntries(t *testing.T) {
	vc := NewVectorClock()
	now := time.Now()
	for i, id := range []node.NodeID{"a", "b", "c"} {
		vc.Set(id, 1)
		vc.entries[id].timestamp = now.Add(-time.Duration(i) * time.Second).Unix()
	}

	removed := vc.Prune(time.Hour, 2)
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}
	if vc.Size() != 2 {
		t.Fatalf("expected 2 entries left, got %d", vc.Size())
	}
	if vc.Get("c") != 0 {
		t.Fatal("expected the oldest entry (c) to be trimmed first")
	}
}

func TestMarshalUnmarshalBinary_RoundTrip(t *testing.T) {
	vc := NewVectorClock()
	vc.Set("node-a", 3)
	vc.Set("node-b", 7)

	data, err := vc.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	var got VectorClock
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if !got.Equals(vc) {
		t.Fatalf("round trip changed causal content: got %+v, want %+v", got, vc)
	}
	if got.Get("node-a") != 3 || got.Get("node-b") != 7 {
		t.Fatalf("counters did not round-trip: a=%d b=%d", got.Get("node-a"), got.Get("node-b"))
	}
}

func TestMarshalBinary_EmptyClock(t *testing.T) {
	vc := NewVectorClock()
	data, err := vc.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	var got VectorClock
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("expected empty round trip, got %d entries", got.Size())
	}
}

func TestUnmarshalBinary_RejectsTruncatedData(t *testing.T) {
	vc := NewVectorClock()
	vc.Set("node-a", 3)
	data, err := vc.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	for n := 0; n < len(data); n++ {
		var got VectorClock
		if err := got.UnmarshalBinary(data[:n]); err == nil {
			t.Fatalf("expected an error decoding %d/%d truncated bytes, got nil", n, len(data))
		}
	}
}

func TestMarshalUnmarshalJSON_RoundTrip(t *testing.T) {
	vc := NewVectorClock()
	vc.Set("node-a", 9)

	data, err := vc.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var got VectorClock
	if err := got.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if got.Get("node-a") != 9 {
		t.Fatalf("expected counter 9, got %d", got.Get("node-a"))
	}
}
