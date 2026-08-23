package pebble

import (
	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter/storage"
)

// Store implements engine/storage.Storage on top of Pebble.
//
// v1 exposed DB() *pebble.DB as a low-level escape hatch for backup
// checkpointing (see engine/storage.Storage's doc comment: a pure-domain
// port cannot reference a concrete external type). Once Pebble is wired in,
// Store should grow its own DB() *pebble.DB method alongside this port so
// infra/backup can checkpoint it directly.
//
// (v1: internal/storage/engine.go Storage)
type Store struct {
	opts Options
}

var _ storage.Storage = (*Store)(nil)

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
func (s *Store) Get(key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// GetRaw returns the sibling set for key with tombstones visible.
//
// TODO(v2): read key from Pebble, decode the sibling set, and filter only
// TTL-expired siblings (tombstones stay visible) (v1:
// internal/storage/engine.go Storage.GetRaw).
func (s *Store) GetRaw(key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// Put reconciles siblings into the store for key.
//
// TODO(v2): read existing siblings via GetRaw, reconcile with the incoming
// siblings by vector clock, prune vector clocks and excess siblings per
// opts, encode the merged set, and write it to Pebble under the configured
// sync mode (v1: internal/storage/engine.go Storage.Put).
func (s *Store) Put(key []byte, siblings *storage.SiblingSet, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// Delete writes a tombstone for key, causally ordered by ctx.
//
// TODO(v2): build a single tombstone Sibling stamped with ctx and the
// current time, and route it through Put (v1: internal/storage/engine.go
// Storage.Delete).
func (s *Store) Delete(key []byte, ctx vclock.VectorClock, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// Scan visits every key in [start, end) in order, invoking fn.
//
// TODO(v2): open a Pebble iterator bounded by [start, end), decode each
// value, and invoke fn until it returns false or the iterator is exhausted
// (v1: internal/storage/engine.go Storage.Scan).
func (s *Store) Scan(start, end []byte, fn storage.ScanFunc, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// LocalNodeID returns the ID of the node this storage engine serves.
//
// TODO(v2): return s.opts.NodeID (v1: internal/storage/engine.go
// Storage.LocalNodeID).
func (s *Store) LocalNodeID() node.NodeID {
	return ""
}

// Stats returns point-in-time storage engine statistics.
//
// TODO(v2): read pebble.DB.Metrics() and count keys via an iterator (v1:
// internal/storage/engine.go Storage.Stats).
func (s *Store) Stats() storage.Stats {
	return storage.Stats{}
}

// Close releases all resources held by the storage engine.
//
// TODO(v2): stop the tombstone-GC loop and close the Pebble database (v1:
// internal/storage/engine.go Storage.Close).
func (s *Store) Close() error {
	return contracts.ErrNotImplemented
}
