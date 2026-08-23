package pebble

import (
	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
)

// Store implements engine/storage.Storage on top of Pebble.
type Store struct {
	opts Options
}

var _ adapter.Storage = (*Store)(nil)

// NewStore opens (or creates) a Pebble database at opts.DataDir.
//
// TODO(v2): import github.com/cockroachdb/pebble; validate opts, build
// pebble.Options from it (cache size, memtable size, compaction
// concurrency, WAL/sync mode), open the database, and start the
// tombstone-GC background loop if opts.TombstoneGCEnabled (v1:
// internal/storage/engine.go NewStorage).
func NewStore(opts Options) (*Store, error) {
	return nil, contracts.ErrNotImplemented
}

// Get returns the sibling set for key, filtering out tombstones and
// expired siblings.
//
// TODO(v2): read key from Pebble, decode the sibling set, filter
// tombstones and TTL-expired siblings, and translate pebble.ErrNotFound to
// quorumerr.ErrKeyNotFound (v1: internal/storage/engine.go Storage.Get).
func (s *Store) Get(key []byte, done func(*adapter.SiblingSet, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// GetRaw returns the sibling set for key with tombstones visible.
func (s *Store) GetRaw(key []byte, done func(*adapter.SiblingSet, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// Put reconciles siblings into the store for key.
func (s *Store) Put(key []byte, siblings *adapter.SiblingSet, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// Delete writes a tombstone for key, causally ordered by ctx.
func (s *Store) Delete(key []byte, ctx vclock.VectorClock, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// Scan visits every key in [start, end) in order, invoking fn.
func (s *Store) Scan(start, end []byte, fn adapter.ScanFunc, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// LocalNodeID returns the ID of the node this storage engine serves.
func (s *Store) LocalNodeID() node.NodeID {
	return ""
}

// Stats returns point-in-time storage engine statistics.
func (s *Store) Stats() adapter.StorageStats {
	return adapter.StorageStats{}
}

// Close releases all resources held by the storage engine.
//
// TODO(v2): stop the tombstone-GC loop and close the Pebble database (v1:
// internal/storage/engine.go Storage.Close).
func (s *Store) Close() error {
	return contracts.ErrNotImplemented
}
