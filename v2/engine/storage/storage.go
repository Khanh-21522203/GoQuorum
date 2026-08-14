package storage

import (
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
)

// Storage is the [PORT] implemented by concrete storage adapters (e.g.
// infra/storage/pebble, infra/storage/journal). engine depends only on
// this interface, never on a concrete engine, so the domain core stays
// free of Pebble/io_uring/I/O concerns (v1: internal/storage/engine.go
// exposed a concrete *storage.Storage).
//
// Every method that may touch disk is callback-based rather than
// blocking: engine subsystems run on a single-threaded
// engine/reactor.Reactor, so no engine call may block waiting on I/O.
// done is invoked exactly once, from the same reactor goroutine that
// issued the call, once the operation completes or fails.
//
// v1's Storage also exposed DB() *pebble.DB as a low-level escape hatch for
// backup/checkpointing. That method is dropped here: *pebble.DB is an
// external, concrete type that cannot appear on a pure-domain port. Infra
// adapters that need it may expose it on their own concrete type alongside
// this interface.
type Storage interface {
	// Get returns the sibling set for key, filtering out tombstones and
	// expired siblings.
	Get(key []byte, done func(*SiblingSet, error))
	// GetRaw returns the sibling set for key with tombstones visible
	// (used internally by read-repair and anti-entropy).
	GetRaw(key []byte, done func(*SiblingSet, error))
	// Put reconciles siblings into the store for key.
	Put(key []byte, siblings *SiblingSet, done func(error))
	// Delete writes a tombstone for key, causally ordered by ctx.
	Delete(key []byte, ctx vclock.VectorClock, done func(error))
	// Scan visits every key in [start, end) in order, invoking fn for
	// each one, then invokes done once the scan completes or fails. fn
	// itself runs synchronously on the reactor goroutine, once per key.
	Scan(start, end []byte, fn ScanFunc, done func(error))
	// LocalNodeID returns the ID of the node this storage engine serves.
	// Pure in-memory value; no I/O, so no callback is needed.
	LocalNodeID() node.NodeID
	// Stats returns point-in-time storage engine statistics. Pure/cached;
	// no I/O, so no callback is needed.
	Stats() Stats
	// Close releases all resources held by the storage engine. Called
	// only after the owning Reactor's Run has returned, so it may block.
	Close() error
}
