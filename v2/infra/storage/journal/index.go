package journal

import (
	"bytes"
	"sort"
)

// indexEntry locates one key's most recently written record on disk.
type indexEntry struct {
	// Offset is the byte offset, from the start of the WAL file, where the
	// record's Length field begins.
	Offset int64
	// Length is the total size of the record in bytes, including its
	// header, i.e. the exact number of bytes to read starting at Offset to
	// recover the whole record.
	Length uint32
	// Tombstone reports whether the last sibling appended by the most
	// recent Put/Delete for this key was a tombstone. See doc.go's
	// "Put/Delete reconciliation policy" section for what this does and
	// does not imply about the record's contents.
	Tombstone bool
}

// index is the in-memory key -> on-disk-location map every Store keeps.
// It holds no I/O state and no lock: like every other reactor-owned type
// in this codebase, it is safe only because a single goroutine ever
// touches it (see doc.go's "Ownership contract" section).
type index struct {
	entries map[string]indexEntry
}

// newIndex returns an empty index.
func newIndex() *index {
	return &index{entries: make(map[string]indexEntry)}
}

// Get returns the entry for key, if any key has ever been written.
func (idx *index) Get(key []byte) (indexEntry, bool) {
	e, ok := idx.entries[string(key)]
	return e, ok
}

// Set records (or overwrites) key's entry.
func (idx *index) Set(key []byte, entry indexEntry) {
	idx.entries[string(key)] = entry
}

// Delete removes key from the index entirely. Store does not use this for
// ordinary application-level deletes (those are tombstone records handled
// via Set, so GetRaw can still see them) — it exists for completeness and
// for tests that need to assert a key is entirely absent.
func (idx *index) Delete(key []byte) {
	delete(idx.entries, string(key))
}

// Len returns the number of distinct keys ever written, including
// currently tombstoned ones.
func (idx *index) Len() int {
	return len(idx.entries)
}

// Keys returns every key in the index, sorted ascending by byte value, for
// use by Scan. The returned slice is a fresh copy the caller may freely
// hold onto or mutate.
func (idx *index) Keys() [][]byte {
	keys := make([][]byte, 0, len(idx.entries))
	for k := range idx.entries {
		keys = append(keys, []byte(k))
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys
}
