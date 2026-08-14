package journal

import "testing"

func TestIndex_SetGetDelete(t *testing.T) {
	idx := newIndex()
	if _, ok := idx.Get([]byte("k")); ok {
		t.Fatal("expected miss on empty index")
	}

	idx.Set([]byte("k"), indexEntry{Offset: 10, Length: 5})
	e, ok := idx.Get([]byte("k"))
	if !ok || e.Offset != 10 || e.Length != 5 {
		t.Fatalf("unexpected entry: %+v ok=%v", e, ok)
	}

	idx.Delete([]byte("k"))
	if _, ok := idx.Get([]byte("k")); ok {
		t.Fatal("expected miss after delete")
	}
}

func TestIndex_SetOverwritesPreviousEntry(t *testing.T) {
	idx := newIndex()
	idx.Set([]byte("k"), indexEntry{Offset: 0, Length: 1})
	idx.Set([]byte("k"), indexEntry{Offset: 100, Length: 2, Tombstone: true})

	e, ok := idx.Get([]byte("k"))
	if !ok || e.Offset != 100 || e.Length != 2 || !e.Tombstone {
		t.Fatalf("expected overwritten entry, got %+v ok=%v", e, ok)
	}
}

func TestIndex_KeysSorted(t *testing.T) {
	idx := newIndex()
	for _, k := range []string{"charlie", "alpha", "bravo"} {
		idx.Set([]byte(k), indexEntry{})
	}
	keys := idx.Keys()
	want := []string{"alpha", "bravo", "charlie"}
	if len(keys) != len(want) {
		t.Fatalf("got %d keys, want %d", len(keys), len(want))
	}
	for i, k := range keys {
		if string(k) != want[i] {
			t.Fatalf("keys[%d] = %q, want %q", i, k, want[i])
		}
	}
}

func TestIndex_Len(t *testing.T) {
	idx := newIndex()
	idx.Set([]byte("a"), indexEntry{})
	idx.Set([]byte("b"), indexEntry{})
	if idx.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", idx.Len())
	}
}
