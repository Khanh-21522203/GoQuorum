// Package vclock implements vector clocks for tracking causality across distributed nodes.
// Designed for 100% zero-allocation operation using flat inline arrays and 2-pointer linear sweeps.
package vclock

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
	"unsafe"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
)

// maxInlineEntries is the fixed capacity of the inline array inside VectorClock.
// For clusters with <= 8 nodes (the standard for partitions/preference lists),
// VectorClock operations produce ZERO heap allocations.
const maxInlineEntries = 8

// Entry is a single per-node counter tracked by a VectorClock, together with
// the timestamp of its last update (used for pruning).
type Entry struct {
	NodeID    node.NodeID
	Counter   uint64
	Timestamp int64 // Unix timestamp (seconds).
}

// VectorClock tracks causality across nodes using per-node Lamport counters.
// Entries are kept sorted by NodeID to enable single-pass O(N) 2-pointer comparisons and merges with 0 allocations.
type VectorClock struct {
	inline [maxInlineEntries]Entry
	count  uint8
	heap   []Entry // Dynamic fallback for clusters with > 8 nodes
}

// NewVectorClock creates an empty vector clock with 0 heap allocations.
func NewVectorClock() VectorClock {
	return VectorClock{}
}

func (vc *VectorClock) entriesSlice() []Entry {
	if vc.count <= maxInlineEntries {
		return vc.inline[:vc.count]
	}
	return vc.heap
}

// find returns the index of id in entries, or the insertion point if not found.
func (vc *VectorClock) find(id node.NodeID) (int, bool) {
	entries := vc.entriesSlice()
	lo, hi := 0, len(entries)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if entries[mid].NodeID < id {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(entries) && entries[lo].NodeID == id {
		return lo, true
	}
	return lo, false
}

func (vc *VectorClock) insertAt(idx int, e Entry) {
	if vc.count < maxInlineEntries {
		for i := int(vc.count); i > idx; i-- {
			vc.inline[i] = vc.inline[i-1]
		}
		vc.inline[idx] = e
		vc.count++
		return
	}

	if vc.count == maxInlineEntries {
		vc.heap = make([]Entry, maxInlineEntries+1)
		copy(vc.heap[:idx], vc.inline[:idx])
		vc.heap[idx] = e
		copy(vc.heap[idx+1:], vc.inline[idx:])
		vc.count++
		return
	}

	vc.heap = append(vc.heap, Entry{})
	copy(vc.heap[idx+1:], vc.heap[idx:])
	vc.heap[idx] = e
	vc.count++
}

// Tick increments the counter for the given node, creating an entry with counter 1 if none exists.
func (vc *VectorClock) Tick(id node.NodeID) {
	idx, found := vc.find(id)
	now := time.Now().Unix()
	if found {
		entries := vc.entriesSlice()
		entries[idx].Counter++
		entries[idx].Timestamp = now
		return
	}
	vc.insertAt(idx, Entry{NodeID: id, Counter: 1, Timestamp: now})
}

// Get returns the counter for the given node, or 0 if absent.
func (vc VectorClock) Get(id node.NodeID) uint64 {
	idx, found := vc.find(id)
	if found {
		return vc.entriesSlice()[idx].Counter
	}
	return 0
}

// Set sets the counter for the given node, creating the entry if absent.
func (vc *VectorClock) Set(id node.NodeID, counter uint64) {
	vc.SetWithTimestamp(id, counter, time.Now().Unix())
}

// SetWithTimestamp sets the counter and explicit timestamp for the given node.
func (vc *VectorClock) SetWithTimestamp(id node.NodeID, counter uint64, ts int64) {
	idx, found := vc.find(id)
	if found {
		entries := vc.entriesSlice()
		entries[idx].Counter = counter
		entries[idx].Timestamp = ts
		return
	}
	vc.insertAt(idx, Entry{NodeID: id, Counter: counter, Timestamp: ts})
}

// Merge merges another vector clock into vc, taking the maximum counter for each node.
func (vc *VectorClock) Merge(other VectorClock) {
	if other.IsEmpty() {
		return
	}
	if vc.IsEmpty() {
		*vc = other.Copy()
		return
	}

	a := vc.entriesSlice()
	b := other.entriesSlice()

	var merged [maxInlineEntries]Entry
	mergedCount := 0
	i, j := 0, 0

	var heapMerged []Entry
	useHeap := (len(a) + len(b)) > maxInlineEntries

	if useHeap {
		heapMerged = make([]Entry, 0, len(a)+len(b))
	}

	appendEntry := func(e Entry) {
		if !useHeap && mergedCount < maxInlineEntries {
			merged[mergedCount] = e
			mergedCount++
		} else {
			if !useHeap {
				useHeap = true
				heapMerged = make([]Entry, 0, len(a)+len(b))
				heapMerged = append(heapMerged, merged[:mergedCount]...)
			}
			heapMerged = append(heapMerged, e)
			mergedCount++
		}
	}

	for i < len(a) && j < len(b) {
		if a[i].NodeID == b[j].NodeID {
			e := a[i]
			if b[j].Counter > e.Counter {
				e.Counter = b[j].Counter
				e.Timestamp = b[j].Timestamp
			} else if b[j].Counter == e.Counter && b[j].Timestamp > e.Timestamp {
				e.Timestamp = b[j].Timestamp
			}
			appendEntry(e)
			i++
			j++
		} else if a[i].NodeID < b[j].NodeID {
			appendEntry(a[i])
			i++
		} else {
			appendEntry(b[j])
			j++
		}
	}

	for ; i < len(a); i++ {
		appendEntry(a[i])
	}
	for ; j < len(b); j++ {
		appendEntry(b[j])
	}

	if !useHeap {
		vc.inline = merged
		vc.count = uint8(mergedCount)
		vc.heap = nil
	} else {
		vc.heap = heapMerged
		vc.count = uint8(len(heapMerged))
	}
}

// Copy returns a deep copy of vc.
func (vc VectorClock) Copy() VectorClock {
	res := vc
	if len(vc.heap) > 0 {
		res.heap = append([]Entry(nil), vc.heap...)
	}
	return res
}

// IsEmpty returns true if the vector clock has no entries.
func (vc VectorClock) IsEmpty() bool {
	return vc.count == 0
}

// Size returns the number of entries in the vector clock.
func (vc VectorClock) Size() int {
	return int(vc.count)
}

// Compare returns the causal relationship between vc and other using a sorted 2-pointer linear scan.
func (vc VectorClock) Compare(other VectorClock) Ordering {
	a := vc.entriesSlice()
	b := other.entriesSlice()

	var lessFound, greaterFound bool
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].NodeID == b[j].NodeID {
			if a[i].Counter < b[j].Counter {
				lessFound = true
			} else if a[i].Counter > b[j].Counter {
				greaterFound = true
			}
			i++
			j++
		} else if a[i].NodeID < b[j].NodeID {
			if a[i].Counter > 0 {
				greaterFound = true
			}
			i++
		} else {
			if b[j].Counter > 0 {
				lessFound = true
			}
			j++
		}
	}

	for ; i < len(a); i++ {
		if a[i].Counter > 0 {
			greaterFound = true
		}
	}
	for ; j < len(b); j++ {
		if b[j].Counter > 0 {
			lessFound = true
		}
	}

	switch {
	case lessFound && greaterFound:
		return Concurrent
	case lessFound:
		return Before
	case greaterFound:
		return After
	default:
		return Equal
	}
}

// HappensBefore reports whether vc happens before other.
func (vc VectorClock) HappensBefore(other VectorClock) bool {
	return vc.Compare(other) == Before
}

// HappensAfter reports whether vc happens after other.
func (vc VectorClock) HappensAfter(other VectorClock) bool {
	return vc.Compare(other) == After
}

// IsConcurrentWith reports whether neither vc nor other dominates the other.
func (vc VectorClock) IsConcurrentWith(other VectorClock) bool {
	return vc.Compare(other) == Concurrent
}

// Equals reports whether vc and other are causally identical.
func (vc VectorClock) Equals(other VectorClock) bool {
	return vc.Compare(other) == Equal
}

// Dominates reports whether vc happens after or equals other.
func (vc VectorClock) Dominates(other VectorClock) bool {
	switch vc.Compare(other) {
	case After, Equal:
		return true
	default:
		return false
	}
}

// Prune removes entries older than threshold, and further trims down to maxEntries.
func (vc *VectorClock) Prune(threshold time.Duration, maxEntries int) int {
	if vc.IsEmpty() {
		return 0
	}

	entries := vc.entriesSlice()
	removed := 0
	cutoff := time.Now().Add(-threshold).Unix()

	writeIdx := 0
	for i := 0; i < len(entries); i++ {
		if entries[i].Timestamp >= cutoff {
			if writeIdx != i {
				entries[writeIdx] = entries[i]
			}
			writeIdx++
		} else {
			removed++
		}
	}
	entries = entries[:writeIdx]

	if maxEntries > 0 && len(entries) > maxEntries {
		ranked := append([]Entry(nil), entries...)
		sort.Slice(ranked, func(i, j int) bool { return ranked[i].Timestamp > ranked[j].Timestamp })
		surviving := ranked[:maxEntries]
		sort.Slice(surviving, func(i, j int) bool { return surviving[i].NodeID < surviving[j].NodeID })
		removed += len(entries) - maxEntries
		entries = surviving
	}

	if len(entries) <= maxInlineEntries {
		copy(vc.inline[:len(entries)], entries)
		vc.count = uint8(len(entries))
		vc.heap = nil
	} else {
		vc.heap = entries
		vc.count = uint8(len(entries))
	}
	return removed
}

// AppendMarshalBinary encodes the vector clock to dst in compact binary format.
func (vc VectorClock) AppendMarshalBinary(dst []byte) ([]byte, error) {
	entries := vc.entriesSlice()
	if len(entries) > math.MaxUint16 {
		return nil, fmt.Errorf("vclock: too many entries to encode: %d", len(entries))
	}

	dst = binary.BigEndian.AppendUint16(dst, uint16(len(entries)))
	for _, e := range entries {
		idBytes := []byte(e.NodeID)
		if len(idBytes) > math.MaxUint16 {
			return nil, fmt.Errorf("vclock: node ID too long to encode: %d bytes", len(idBytes))
		}
		dst = binary.BigEndian.AppendUint16(dst, uint16(len(idBytes)))
		dst = append(dst, idBytes...)
		dst = binary.BigEndian.AppendUint64(dst, e.Counter)
		dst = binary.BigEndian.AppendUint64(dst, uint64(e.Timestamp))
	}
	return dst, nil
}

// MarshalBinary encodes the vector clock to a newly allocated binary buffer.
func (vc VectorClock) MarshalBinary() ([]byte, error) {
	entries := vc.entriesSlice()
	dst := make([]byte, 0, 2+32*len(entries))
	return vc.AppendMarshalBinary(dst)
}

// UnmarshalBinary decodes a vector clock previously produced by MarshalBinary.
func (vc *VectorClock) UnmarshalBinary(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: vector clock header truncated", quorumerr.ErrCorruptedData)
	}
	count := int(binary.BigEndian.Uint16(data))
	data = data[2:]

	vc.count = 0
	vc.heap = nil

	for i := 0; i < count; i++ {
		if len(data) < 2 {
			return fmt.Errorf("%w: node ID length truncated", quorumerr.ErrCorruptedData)
		}
		idLen := int(binary.BigEndian.Uint16(data))
		data = data[2:]
		if idLen > len(data) {
			return fmt.Errorf("%w: node ID truncated", quorumerr.ErrCorruptedData)
		}
		var id node.NodeID
		if idLen > 0 {
			id = node.NodeID(unsafe.String(unsafe.SliceData(data[:idLen]), idLen))
		}
		data = data[idLen:]

		const trailerLen = 8 + 8
		if len(data) < trailerLen {
			return fmt.Errorf("%w: entry trailer truncated", quorumerr.ErrCorruptedData)
		}
		counter := binary.BigEndian.Uint64(data)
		data = data[8:]
		timestamp := int64(binary.BigEndian.Uint64(data))
		data = data[8:]

		e := Entry{NodeID: id, Counter: counter, Timestamp: timestamp}
		if vc.count < maxInlineEntries {
			vc.inline[vc.count] = e
			vc.count++
		} else {
			if len(vc.heap) == 0 {
				vc.heap = make([]Entry, maxInlineEntries, count)
				copy(vc.heap, vc.inline[:maxInlineEntries])
			}
			vc.heap = append(vc.heap, e)
			vc.count = uint8(len(vc.heap))
		}
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (vc VectorClock) MarshalJSON() ([]byte, error) {
	entries := vc.entriesSlice()
	m := make(map[string]uint64, len(entries))
	for _, e := range entries {
		m[string(e.NodeID)] = e.Counter
	}
	return json.Marshal(m)
}

// UnmarshalJSON implements json.Unmarshaler.
func (vc *VectorClock) UnmarshalJSON(data []byte) error {
	var m map[string]uint64
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	now := time.Now().Unix()
	vc.count = 0
	vc.heap = nil
	for id, counter := range m {
		vc.Set(node.NodeID(id), counter)
	}
	entries := vc.entriesSlice()
	for i := range entries {
		entries[i].Timestamp = now
	}
	return nil
}
