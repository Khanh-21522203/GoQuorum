package journal

import (
	"bytes"
	"sort"
)

// indexEntry locates one key's latest record on disk.
type indexEntry struct {
	Offset int64  // File offset where the record begins
	Length uint32 // Total record size in bytes
}

// index is an in-memory key -> on-disk location map.
// Not safe for concurrent use; accessed only on the reactor thread.
type index struct {
	entries map[string]indexEntry
}

// newIndex returns an empty in-memory index.
func newIndex() *index {
	return &index{entries: make(map[string]indexEntry)}
}

// Get returns the on-disk index entry for key, if present.
func (idx *index) Get(key []byte) (indexEntry, bool) {
	e, ok := idx.entries[string(key)]
	return e, ok
}

// Set records or updates the on-disk location for key.
func (idx *index) Set(key []byte, entry indexEntry) {
	idx.entries[string(key)] = entry
}

// Delete removes key from the in-memory index.
func (idx *index) Delete(key []byte) {
	delete(idx.entries, string(key))
}

// Len returns the total number of indexed keys.
func (idx *index) Len() int {
	return len(idx.entries)
}

// Keys returns all indexed keys sorted in ascending lexicographical order.
func (idx *index) Keys() [][]byte {
	keys := make([][]byte, 0, len(idx.entries))
	for k := range idx.entries {
		keys = append(keys, []byte(k))
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys
}
