package storage

import (
	"errors"
	"sort"
	"testing"
	"time"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/infra/storage/journal"
)

type memoryKV struct {
	data            map[string][]byte
	onReadComplete  func(reqID uint64, key []byte, val []byte, err error)
	onWriteComplete func(reqID uint64, key []byte, err error)
	onScanComplete  func(scanID uint64, items []journal.ScanEntry, err error)
}

func newMemoryKV() *memoryKV {
	return &memoryKV{data: make(map[string][]byte)}
}

func (m *memoryKV) SetOnReadComplete(fn func(reqID uint64, key []byte, val []byte, err error)) {
	m.onReadComplete = fn
}

func (m *memoryKV) SetOnWriteComplete(fn func(reqID uint64, key []byte, err error)) {
	m.onWriteComplete = fn
}

func (m *memoryKV) SetOnScanComplete(fn func(scanID uint64, items []journal.ScanEntry, err error)) {
	m.onScanComplete = fn
}

func (m *memoryKV) SetOnStorageError(fn func(err error)) {
}

func (m *memoryKV) Get(reqID uint64, key []byte) error {
	val, ok := m.data[string(key)]
	if !ok {
		if m.onReadComplete != nil {
			m.onReadComplete(reqID, key, nil, quorumerr.ErrKeyNotFound)
		}
		return nil
	}
	if m.onReadComplete != nil {
		m.onReadComplete(reqID, key, val, nil)
	}
	return nil
}

func (m *memoryKV) Put(reqID uint64, key []byte, val []byte) error {
	m.data[string(key)] = append([]byte(nil), val...)
	if m.onWriteComplete != nil {
		m.onWriteComplete(reqID, key, nil)
	}
	return nil
}

func (m *memoryKV) Delete(reqID uint64, key []byte) error {
	delete(m.data, string(key))
	if m.onWriteComplete != nil {
		m.onWriteComplete(reqID, key, nil)
	}
	return nil
}

func (m *memoryKV) Scan(scanID uint64, start, end []byte) error {
	var keys []string
	for k := range m.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var items []journal.ScanEntry
	for _, k := range keys {
		if len(start) > 0 && k < string(start) {
			continue
		}
		if len(end) > 0 && k >= string(end) {
			continue
		}
		items = append(items, journal.ScanEntry{
			Key:   []byte(k),
			Value: m.data[k],
		})
	}
	if m.onScanComplete != nil {
		m.onScanComplete(scanID, items, nil)
	}
	return nil
}

func (m *memoryKV) Close() error {
	return nil
}

func TestAdapter_PutGetDeleteRoundTrip(t *testing.T) {
	kv := newMemoryKV()
	adapter := NewAdapter(kv, "node-1")

	if adapter.LocalNodeID() != "node-1" {
		t.Fatalf("LocalNodeID = %q, want node-1", adapter.LocalNodeID())
	}

	key := []byte("user:1")
	vc := vclock.NewVectorClock()
	vc.Set("node-1", 1)
	ss := &SiblingSet{
		Siblings: []Sibling{{
			Value:     []byte("Alice"),
			VClock:    vc,
			Timestamp: time.Now().Unix(),
		}},
	}

	var putErr error
	adapter.Put(key, ss, func(err error) { putErr = err })
	if putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	var got *SiblingSet
	var getErr error
	adapter.Get(key, func(res *SiblingSet, err error) {
		got = res
		getErr = err
	})
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if len(got.Siblings) != 1 || string(got.Siblings[0].Value) != "Alice" {
		t.Fatalf("unexpected Get result: %+v", got)
	}

	// Delete
	var delErr error
	adapter.Delete(key, vc, func(err error) { delErr = err })
	if delErr != nil {
		t.Fatalf("Delete: %v", delErr)
	}

	// Get should return ErrKeyNotFound (tombstone filtered out)
	adapter.Get(key, func(res *SiblingSet, err error) {
		got = res
		getErr = err
	})
	if !errors.Is(getErr, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after Delete, got %v", getErr)
	}

	// GetRaw should return the tombstone sibling
	var raw *SiblingSet
	var rawErr error
	adapter.GetRaw(key, func(res *SiblingSet, err error) {
		raw = res
		rawErr = err
	})
	if rawErr != nil {
		t.Fatalf("GetRaw: %v", rawErr)
	}
	if len(raw.Siblings) < 1 || !raw.Siblings[len(raw.Siblings)-1].Tombstone {
		t.Fatalf("expected tombstone in GetRaw, got %+v", raw)
	}
}

func TestAdapter_Scan(t *testing.T) {
	kv := newMemoryKV()
	adapter := NewAdapter(kv, "node-1")

	for _, k := range []string{"c", "a", "b"} {
		ss := &SiblingSet{Siblings: []Sibling{{Value: []byte("val-" + k)}}}
		adapter.Put([]byte(k), ss, func(err error) {})
	}

	var scanned []string
	adapter.Scan(nil, nil, func(key []byte, ss *SiblingSet) bool {
		scanned = append(scanned, string(key))
		return true
	}, func(err error) {})

	if len(scanned) != 3 || scanned[0] != "a" || scanned[1] != "b" || scanned[2] != "c" {
		t.Fatalf("unexpected scan results: %v", scanned)
	}
}
