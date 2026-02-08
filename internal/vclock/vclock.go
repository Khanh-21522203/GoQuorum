package vclock

import (
	"GoQuorum/internal/common"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// VectorClockEntry includes timestamp for pruning (Section 4.3)
type VectorClockEntry struct {
	NodeID    common.NodeID
	Counter   uint64
	Timestamp int64 // Unix timestamp (seconds) for pruning
}

// VectorClock tracks causality using Lamport timestamps per node
// Structure: map[NodeID]Counter with timestamps (Section 4.1)
type VectorClock struct {
	entries map[common.NodeID]*VectorClockEntry
}

// NewVectorClock creates an empty vector clock
func NewVectorClock() VectorClock {
	return VectorClock{
		entries: make(map[common.NodeID]*VectorClockEntry),
	}
}

// Entries returns a map of node IDs to counters (for API serialization)
func (vc VectorClock) Entries() map[string]uint64 {
	result := make(map[string]uint64, len(vc.entries))
	for nodeID, entry := range vc.entries {
		result[string(nodeID)] = entry.Counter
	}
	return result
}

// SetString sets counter for node using string node ID (for API compatibility)
func (vc *VectorClock) SetString(nodeID string, counter uint64) {
	vc.Set(common.NodeID(nodeID), counter)
}

// ===== BASIC OPERATIONS =====

// Tick increments counter for given node (Section 4.2 - Increment operation)
func (vc *VectorClock) Tick(nodeID common.NodeID) {
	if entry, exists := vc.entries[nodeID]; exists {
		entry.Counter++
		entry.Timestamp = time.Now().Unix()
	} else {
		vc.entries[nodeID] = &VectorClockEntry{
			NodeID:    nodeID,
			Counter:   1,
			Timestamp: time.Now().Unix(),
		}
	}
}

// Get returns counter for node (0 if not present)
func (vc VectorClock) Get(nodeID common.NodeID) uint64 {
	if entry, exists := vc.entries[nodeID]; exists {
		return entry.Counter
	}
	return 0
}

// Set sets counter for node
func (vc *VectorClock) Set(nodeID common.NodeID, counter uint64) {
	if entry, exists := vc.entries[nodeID]; exists {
		entry.Counter = counter
		entry.Timestamp = time.Now().Unix()
	} else {
		vc.entries[nodeID] = &VectorClockEntry{
			NodeID:    nodeID,
			Counter:   counter,
			Timestamp: time.Now().Unix(),
		}
	}
}

// Merge merges another vector clock (Section 4.2 - Merge operation)
// Takes maximum counter for each node
func (vc *VectorClock) Merge(other VectorClock) {
	for nodeID, otherEntry := range other.entries {
		if myEntry, exists := vc.entries[nodeID]; exists {
			if otherEntry.Counter > myEntry.Counter {
				myEntry.Counter = otherEntry.Counter
				myEntry.Timestamp = otherEntry.Timestamp
			} else if otherEntry.Counter == myEntry.Counter {
				// Keep newer timestamp
				if otherEntry.Timestamp > myEntry.Timestamp {
					myEntry.Timestamp = otherEntry.Timestamp
				}
			}
		} else {
			// Node not present, add it
			vc.entries[nodeID] = &VectorClockEntry{
				NodeID:    otherEntry.NodeID,
				Counter:   otherEntry.Counter,
				Timestamp: otherEntry.Timestamp,
			}
		}
	}
}

// Copy creates a deep copy of the vector clock
func (vc VectorClock) Copy() VectorClock {
	newVC := NewVectorClock()
	for nodeID, entry := range vc.entries {
		newVC.entries[nodeID] = &VectorClockEntry{
			NodeID:    entry.NodeID,
			Counter:   entry.Counter,
			Timestamp: entry.Timestamp,
		}
	}
	return newVC
}

// IsEmpty returns true if vector clock has no entries
func (vc VectorClock) IsEmpty() bool {
	return len(vc.entries) == 0
}

// Size returns number of entries
func (vc VectorClock) Size() int {
	return len(vc.entries)
}

// ===== PRUNING (Section 4.3) =====

// Prune removes old entries (Section 4.3 - Vector Clock Size Management)
// Returns number of entries pruned
func (vc *VectorClock) Prune(threshold time.Duration, maxEntries int) int {
	now := time.Now().Unix()
	pruneThresholdSec := int64(threshold.Seconds())

	pruned := 0

	// Step 1: Remove entries older than threshold
	for nodeID, entry := range vc.entries {
		if (now - entry.Timestamp) > pruneThresholdSec {
			delete(vc.entries, nodeID)
			pruned++
		}
	}

	// Step 2: If still too many, keep the newest entries only
	if len(vc.entries) > maxEntries {
		// Sort entries by timestamp (newest first)
		type kv struct {
			nodeID common.NodeID
			entry  *VectorClockEntry
		}

		sorted := make([]kv, 0, len(vc.entries))
		for k, v := range vc.entries {
			sorted = append(sorted, kv{k, v})
		}

		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].entry.Timestamp > sorted[j].entry.Timestamp
		})

		// Keep only maxEntries newest
		newEntries := make(map[common.NodeID]*VectorClockEntry, maxEntries)
		for i := 0; i < maxEntries && i < len(sorted); i++ {
			newEntries[sorted[i].nodeID] = sorted[i].entry
		}

		pruned += len(vc.entries) - len(newEntries)
		vc.entries = newEntries
	}

	return pruned
}

// ===== STRING REPRESENTATION =====

// String returns human-readable representation
// Format: "[node1:5@1704067200, node2:3@1704067100]"
func (vc VectorClock) String() string {
	if len(vc.entries) == 0 {
		return "[]"
	}

	// Sort for deterministic output
	sorted := make([]common.NodeID, 0, len(vc.entries))
	for nodeID := range vc.entries {
		sorted = append(sorted, nodeID)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return string(sorted[i]) < string(sorted[j])
	})

	var buf bytes.Buffer
	buf.WriteString("[")

	for i, nodeID := range sorted {
		if i > 0 {
			buf.WriteString(", ")
		}
		entry := vc.entries[nodeID]
		buf.WriteString(fmt.Sprintf("%s:%d@%d", nodeID, entry.Counter, entry.Timestamp))
	}

	buf.WriteString("]")
	return buf.String()
}

// ===== BINARY ENCODING (for storage) =====

// MarshalBinary encodes VectorClock to binary format with timestamps
// Format (Section 4.3):
//
//	[entry_count: 2 bytes]
//	For each entry:
//	  [node_id_len: 2 bytes]
//	  [node_id: bytes]
//	  [counter: 8 bytes]
//	  [timestamp: 8 bytes]
func (vc VectorClock) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)

	// Write entry count (2 bytes)
	entryCount := len(vc.entries)
	if entryCount > 65535 {
		return nil, fmt.Errorf("too many entries: %d (max: 65535)", entryCount)
	}

	if err := binary.Write(buf, binary.LittleEndian, uint16(entryCount)); err != nil {
		return nil, fmt.Errorf("write entry count: %w", err)
	}

	// Sort keys for deterministic encoding
	sorted := make([]common.NodeID, 0, len(vc.entries))
	for nodeID := range vc.entries {
		sorted = append(sorted, nodeID)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return string(sorted[i]) < string(sorted[j])
	})

	// Write each entry with timestamp
	for _, nodeID := range sorted {
		entry := vc.entries[nodeID]

		// NodeID length (2 bytes)
		nodeIDBytes := []byte(nodeID)
		if len(nodeIDBytes) > 65535 {
			return nil, fmt.Errorf("node_id too long: %d bytes", len(nodeIDBytes))
		}

		if err := binary.Write(buf, binary.LittleEndian, uint16(len(nodeIDBytes))); err != nil {
			return nil, fmt.Errorf("write node_id length: %w", err)
		}

		// NodeID bytes
		if _, err := buf.Write(nodeIDBytes); err != nil {
			return nil, fmt.Errorf("write node_id: %w", err)
		}

		// Counter (8 bytes)
		if err := binary.Write(buf, binary.LittleEndian, entry.Counter); err != nil {
			return nil, fmt.Errorf("write counter: %w", err)
		}

		// Timestamp (8 bytes) - Section 4.3
		if err := binary.Write(buf, binary.LittleEndian, entry.Timestamp); err != nil {
			return nil, fmt.Errorf("write timestamp: %w", err)
		}
	}

	return buf.Bytes(), nil
}

// UnmarshalBinary decodes VectorClock from binary format
func (vc *VectorClock) UnmarshalBinary(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("data too short: %d bytes", len(data))
	}

	buf := bytes.NewReader(data)

	// Read entry count (2 bytes)
	var entryCount uint16
	if err := binary.Read(buf, binary.LittleEndian, &entryCount); err != nil {
		return fmt.Errorf("read entry count: %w", err)
	}

	// Initialize map
	vc.entries = make(map[common.NodeID]*VectorClockEntry, entryCount)

	// Read each entry
	for i := 0; i < int(entryCount); i++ {
		// NodeID length (2 bytes)
		var nodeIDLen uint16
		if err := binary.Read(buf, binary.LittleEndian, &nodeIDLen); err != nil {
			return fmt.Errorf("read node_id length at entry %d: %w", i, err)
		}

		if nodeIDLen == 0 || nodeIDLen > 1024 {
			return fmt.Errorf("invalid node_id length: %d", nodeIDLen)
		}

		// NodeID bytes
		nodeIDBytes := make([]byte, nodeIDLen)
		if _, err := buf.Read(nodeIDBytes); err != nil {
			return fmt.Errorf("read node_id at entry %d: %w", i, err)
		}
		nodeID := common.NodeID(nodeIDBytes)

		// Counter (8 bytes)
		var counter uint64
		if err := binary.Read(buf, binary.LittleEndian, &counter); err != nil {
			return fmt.Errorf("read counter at entry %d: %w", i, err)
		}

		// Timestamp (8 bytes)
		var timestamp int64
		if err := binary.Read(buf, binary.LittleEndian, &timestamp); err != nil {
			return fmt.Errorf("read timestamp at entry %d: %w", i, err)
		}

		vc.entries[nodeID] = &VectorClockEntry{
			NodeID:    nodeID,
			Counter:   counter,
			Timestamp: timestamp,
		}
	}

	return nil
}

// ===== JSON ENCODING (for API/debugging) =====

// MarshalJSON implements json.Marshaler
// Format: {"node1": 5, "node2": 3}
func (vc VectorClock) MarshalJSON() ([]byte, error) {
	m := make(map[common.NodeID]uint64, len(vc.entries))
	for nodeID, entry := range vc.entries {
		m[nodeID] = entry.Counter
	}
	return json.Marshal(m)
}

// UnmarshalJSON implements json.Unmarshaler
func (vc *VectorClock) UnmarshalJSON(data []byte) error {
	m := make(map[common.NodeID]uint64)
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}

	vc.entries = make(map[common.NodeID]*VectorClockEntry, len(m))
	now := time.Now().Unix()

	for nodeID, counter := range m {
		vc.entries[nodeID] = &VectorClockEntry{
			NodeID:    nodeID,
			Counter:   counter,
			Timestamp: now, // Assume current time for JSON deserialization
		}
	}

	return nil
}

// ===== DETAILED JSON (with timestamps for debugging) =====

type detailedJSON struct {
	Entries []VectorClockEntry `json:"entries"`
}

// MarshalDetailed returns JSON with timestamps
// Format: {"entries": [{"node_id": "node1", "counter": 5, "timestamp": 1704067200}]}
func (vc VectorClock) MarshalDetailed() ([]byte, error) {
	entries := make([]VectorClockEntry, 0, len(vc.entries))
	for _, entry := range vc.entries {
		entries = append(entries, *entry)
	}

	// Sort for deterministic output
	sort.Slice(entries, func(i, j int) bool {
		return string(entries[i].NodeID) < string(entries[j].NodeID)
	})

	return json.Marshal(detailedJSON{Entries: entries})
}

// UnmarshalDetailed decodes JSON with timestamps
func (vc *VectorClock) UnmarshalDetailed(data []byte) error {
	var detailed detailedJSON
	if err := json.Unmarshal(data, &detailed); err != nil {
		return err
	}

	vc.entries = make(map[common.NodeID]*VectorClockEntry, len(detailed.Entries))
	for _, entry := range detailed.Entries {
		vc.entries[entry.NodeID] = &VectorClockEntry{
			NodeID:    entry.NodeID,
			Counter:   entry.Counter,
			Timestamp: entry.Timestamp,
		}
	}

	return nil
}
