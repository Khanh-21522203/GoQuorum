package vclock

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
)

// entry is a single per-node counter tracked by a VectorClock, together with
// the timestamp of its last update (used for pruning).
type entry struct {
	counter   uint64
	timestamp int64 // Unix timestamp (seconds).
}

// VectorClock tracks causality across nodes using per-node Lamport
// counters. See the package doc for value-semantics rules: copy with
// Copy(), do not rely on plain assignment for isolation.
type VectorClock struct {
	entries map[node.NodeID]*entry
}

// NewVectorClock creates an empty vector clock.
func NewVectorClock() VectorClock {
	return VectorClock{entries: make(map[node.NodeID]*entry)}
}

// Tick increments the counter for the given node, creating an entry with
// counter 1 if none exists.
func (vc *VectorClock) Tick(id node.NodeID) {
	if vc.entries == nil {
		vc.entries = make(map[node.NodeID]*entry)
	}
	e, ok := vc.entries[id]
	if !ok {
		e = &entry{}
		vc.entries[id] = e
	}
	e.counter++
	e.timestamp = time.Now().Unix()
}

// Get returns the counter for the given node, or 0 if absent.
func (vc VectorClock) Get(id node.NodeID) uint64 {
	if e, ok := vc.entries[id]; ok {
		return e.counter
	}
	return 0
}

// Set sets the counter for the given node, creating the entry if absent.
func (vc *VectorClock) Set(id node.NodeID, counter uint64) {
	if vc.entries == nil {
		vc.entries = make(map[node.NodeID]*entry)
	}
	e, ok := vc.entries[id]
	if !ok {
		e = &entry{}
		vc.entries[id] = e
	}
	e.counter = counter
	e.timestamp = time.Now().Unix()
}

// Merge merges another vector clock into vc, taking the maximum counter for
// each node.
func (vc *VectorClock) Merge(other VectorClock) {
	if len(other.entries) == 0 {
		return
	}
	if vc.entries == nil {
		vc.entries = make(map[node.NodeID]*entry, len(other.entries))
	}
	for id, oe := range other.entries {
		e, ok := vc.entries[id]
		if !ok {
			vc.entries[id] = &entry{counter: oe.counter, timestamp: oe.timestamp}
			continue
		}
		switch {
		case oe.counter > e.counter:
			e.counter = oe.counter
			e.timestamp = oe.timestamp
		case oe.counter == e.counter && oe.timestamp > e.timestamp:
			e.timestamp = oe.timestamp
		}
	}
}

// Copy returns a deep copy of vc: the returned VectorClock owns an
// independent map, so mutating it never affects vc (see package doc).
func (vc VectorClock) Copy() VectorClock {
	entries := make(map[node.NodeID]*entry, len(vc.entries))
	for id, e := range vc.entries {
		entries[id] = &entry{counter: e.counter, timestamp: e.timestamp}
	}
	return VectorClock{entries: entries}
}

// IsEmpty returns true if the vector clock has no entries.
func (vc VectorClock) IsEmpty() bool {
	return len(vc.entries) == 0
}

// Size returns the number of entries in the vector clock.
func (vc VectorClock) Size() int {
	return len(vc.entries)
}

// Compare returns the causal relationship between vc and other, by
// comparing per-node counters over the union of both entry sets.
func (vc VectorClock) Compare(other VectorClock) Ordering {
	seen := make(map[node.NodeID]struct{}, len(vc.entries)+len(other.entries))
	for id := range vc.entries {
		seen[id] = struct{}{}
	}
	for id := range other.entries {
		seen[id] = struct{}{}
	}

	var lessFound, greaterFound bool
	for id := range seen {
		a, b := vc.Get(id), other.Get(id)
		switch {
		case a < b:
			lessFound = true
		case a > b:
			greaterFound = true
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

// IsConcurrentWith reports whether neither vc nor other dominates the
// other.
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

// Prune removes entries older than threshold, and further trims down to
// maxEntries by discarding the oldest by timestamp. A non-positive
// maxEntries disables the size-based trim. Returns the number of entries
// removed.
func (vc *VectorClock) Prune(threshold time.Duration, maxEntries int) int {
	if len(vc.entries) == 0 {
		return 0
	}

	removed := 0
	cutoff := time.Now().Add(-threshold).Unix()
	for id, e := range vc.entries {
		if e.timestamp < cutoff {
			delete(vc.entries, id)
			removed++
		}
	}

	if maxEntries > 0 && len(vc.entries) > maxEntries {
		type ranked struct {
			id        node.NodeID
			timestamp int64
		}
		all := make([]ranked, 0, len(vc.entries))
		for id, e := range vc.entries {
			all = append(all, ranked{id: id, timestamp: e.timestamp})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].timestamp > all[j].timestamp })
		for _, r := range all[maxEntries:] {
			delete(vc.entries, r.id)
			removed++
		}
	}
	return removed
}

// MarshalBinary encodes the vector clock to a compact binary format for
// storage, with entries sorted by node ID for a deterministic encoding.
//
// Layout: [count uint16]{[nodeIDLen uint16][nodeID][counter uint64]
// [timestamp int64]}*count
func (vc VectorClock) MarshalBinary() ([]byte, error) {
	ids := make([]node.NodeID, 0, len(vc.entries))
	for id := range vc.entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > math.MaxUint16 {
		return nil, fmt.Errorf("vclock: too many entries to encode: %d", len(ids))
	}

	buf := make([]byte, 2, 32*len(ids)+2)
	binary.BigEndian.PutUint16(buf, uint16(len(ids)))
	for _, id := range ids {
		e := vc.entries[id]
		idBytes := []byte(id)
		if len(idBytes) > math.MaxUint16 {
			return nil, fmt.Errorf("vclock: node ID too long to encode: %d bytes", len(idBytes))
		}
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(idBytes)))
		buf = append(buf, idBytes...)
		buf = binary.BigEndian.AppendUint64(buf, e.counter)
		buf = binary.BigEndian.AppendUint64(buf, uint64(e.timestamp))
	}
	return buf, nil
}

// UnmarshalBinary decodes a vector clock previously produced by
// MarshalBinary. It never panics on truncated or malformed input, since it
// must safely reject data from an untrusted peer or a torn disk write.
func (vc *VectorClock) UnmarshalBinary(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: vector clock header truncated", quorumerr.ErrCorruptedData)
	}
	count := binary.BigEndian.Uint16(data)
	data = data[2:]

	entries := make(map[node.NodeID]*entry, count)
	for i := uint16(0); i < count; i++ {
		if len(data) < 2 {
			return fmt.Errorf("%w: node ID length truncated", quorumerr.ErrCorruptedData)
		}
		idLen := binary.BigEndian.Uint16(data)
		data = data[2:]
		if uint64(idLen) > uint64(len(data)) {
			return fmt.Errorf("%w: node ID truncated", quorumerr.ErrCorruptedData)
		}
		id := node.NodeID(data[:idLen])
		data = data[idLen:]

		const trailerLen = 8 + 8 // counter + timestamp
		if len(data) < trailerLen {
			return fmt.Errorf("%w: entry trailer truncated", quorumerr.ErrCorruptedData)
		}
		counter := binary.BigEndian.Uint64(data)
		data = data[8:]
		timestamp := int64(binary.BigEndian.Uint64(data))
		data = data[8:]

		entries[id] = &entry{counter: counter, timestamp: timestamp}
	}
	vc.entries = entries
	return nil
}

// MarshalJSON implements json.Marshaler, encoding as {"node": counter, ...}.
func (vc VectorClock) MarshalJSON() ([]byte, error) {
	m := make(map[string]uint64, len(vc.entries))
	for id, e := range vc.entries {
		m[string(id)] = e.counter
	}
	return json.Marshal(m)
}

// UnmarshalJSON implements json.Unmarshaler, stamping the current time on
// every decoded entry (the JSON form carries no timestamps).
func (vc *VectorClock) UnmarshalJSON(data []byte) error {
	var m map[string]uint64
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	now := time.Now().Unix()
	entries := make(map[node.NodeID]*entry, len(m))
	for id, counter := range m {
		entries[node.NodeID(id)] = &entry{counter: counter, timestamp: now}
	}
	vc.entries = entries
	return nil
}
