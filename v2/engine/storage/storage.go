package storage

import (
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/infra/storage/journal"
)

// KVStore is the minimal low-level event-driven byte-oriented storage port
// implemented by infra storage engines (e.g. infra/storage/journal).
type KVStore interface {
	Get(reqID uint64, key []byte) error
	Put(reqID uint64, key []byte, val []byte) error
	Delete(reqID uint64, key []byte) error
	Scan(scanID uint64, start, end []byte) error
	Compact(compactID uint64, filter journal.CompactFilter) error
	SetOnReadComplete(fn func(reqID uint64, key []byte, val []byte, err error))
	SetOnWriteComplete(fn func(reqID uint64, key []byte, err error))
	SetOnScanComplete(fn func(scanID uint64, items []journal.ScanEntry, err error))
	SetOnCompactComplete(fn func(compactID uint64, stats journal.CompactStats, err error))
	SetOnStorageError(fn func(err error))
	Close() error
}

// Storage is the domain port implemented by storage adapters (e.g. Adapter over KVStore).
// The engine layer depends only on this interface.
type Storage interface {
	// Get returns the sibling set for key, filtering out tombstones and expired siblings.
	Get(key []byte, done func(*SiblingSet, error))
	// GetRaw returns the sibling set for key with tombstones visible (used by read-repair/anti-entropy).
	GetRaw(key []byte, done func(*SiblingSet, error))
	// Put reconciles siblings into the store for key.
	Put(key []byte, siblings *SiblingSet, done func(error))
	// Delete writes a tombstone for key, causally ordered by ctx.
	Delete(key []byte, ctx vclock.VectorClock, done func(error))
	// Scan visits every key in [start, end) in order, invoking fn for each one.
	Scan(start, end []byte, fn ScanFunc, done func(error))
	// LocalNodeID returns the ID of the node this storage engine serves.
	LocalNodeID() node.NodeID
	// Stats returns point-in-time storage engine statistics.
	Stats() Stats
	// Close releases all resources held by the storage engine.
	Close() error
}
