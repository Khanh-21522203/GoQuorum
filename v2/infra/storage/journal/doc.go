// Package journal implements engine/storage.Storage as an append-only,
// io_uring-native write-ahead log with an in-memory key -> offset index.
//
// # Design
//
// Every Put or Delete appends one new, self-describing record to the tail
// of a single on-disk file (see record.go for the exact byte layout) and
// then records the record's offset and length under its key in an
// in-memory index (see index.go). Get and Scan never touch the index's
// values directly; they use the index only to locate a record, then read
// that record back off disk. There is no separate "value log" and "index
// file" the way some designs split those concerns — the WAL file is the
// only persisted state, and the index is rebuilt from it by sequential
// replay (see replay.go) every time the store is opened.
//
// # Explicit non-goals for this pass
//
//   - No compaction and no segment rotation: the file only ever grows.
//     Deleted and superseded records are never reclaimed.
//   - No background space reclamation of any kind (no tombstone GC).
//   - No checksumming of the index itself: it is pure derived state,
//     rebuilt from the WAL by Replay on every open.
//   - A submit failure for a write leaves an allocated-but-unwritten gap
//     in the file at that offset (see the "write offset" note below). Real
//     disk write failures are rare enough, and this pass has no
//     compaction to reclaim the gap anyway, that this is accepted as a
//     known limitation rather than engineered around.
//
// # Ownership contract: HandleCompletion
//
// Store has no ability to run an event loop itself. Its methods only
// SUBMIT io_uring operations (via the ioruntime.Runtime passed to Open)
// and record a completion callback keyed by a per-operation user-data
// value. Something else — the reactor.Reactor that owns the same
// ioruntime.Runtime — must actually poll for completions and deliver them
// to Store. The wiring contract is:
//
//	store, _ := journal.Open(rt, opts)
//	r := reactor.New(rt)
//	r.SetEventHandler(store.HandleCompletion)
//	go r.Run()
//
// Every method on Store (Put, Get, GetRaw, Delete, Scan) must therefore be
// called from the same goroutine that calls r.Run() — i.e. from within
// r.Run() itself, from a reactor timer callback, from another Event
// handler, or from a func posted via r.PostFunc — exactly like every other
// reactor-owned type in this codebase. Store keeps no lock of its own: it
// relies entirely on that single-goroutine discipline, the same
// convention engine/reactor.Reactor itself documents.
//
// # Put/Delete reconciliation policy
//
// Put's contract (engine/storage.Storage) is to "reconcile siblings into
// the store for key". This implementation's policy is a simple append
// union: on a Put (or a Delete, which is implemented as a Put of a single
// tombstone Sibling — see Delete), the previously stored sibling set for
// that key is read back (with only TTL-expired siblings dropped, i.e. the
// same visibility GetRaw offers), the incoming siblings are appended to
// the end of that list, and the concatenated set is written out as one
// new record. Siblings are never deduplicated or pruned by vector-clock
// dominance in this pass — that is a real simplification (a client that
// resolves and re-writes a sibling set will see its old siblings persist
// alongside the new one) but it never silently drops a concurrent write,
// which is the property the storage.Storage port doc calls out as
// required.
//
// The index tracks, per key, whether that key is presently tombstoned —
// defined as: the last sibling appended by the most recent Put/Delete had
// Tombstone set. This lets Get answer "not found" for a deleted key
// without a disk read, while GetRaw (used by read-repair/anti-entropy,
// per its doc comment) only checks whether the key was ever written at
// all, so it can still surface a tombstoned record's contents. A Put
// issued after a Delete un-tombstones the key, since it appends a
// non-tombstone sibling as the new last entry.
//
// # Write offset allocation
//
// The Store field tracking the WAL's write offset is advanced at SUBMIT
// time, before the write's completion is known, rather than waiting for
// the completion to land. This is a deliberate choice: Storage's methods
// may be called more than once before an earlier call's completion
// arrives (nothing in the port's contract serializes that), and advancing
// the offset only on completion would let two in-flight writes both
// target the same, still-unclaimed offset — exactly the overlapping
// writes the design is meant to avoid. Reserving the slot at submit time
// keeps offsets monotonic and writes non-overlapping regardless of
// completion order; the in-memory index (the durable, externally visible
// state) is only updated once a write's completion actually reports
// success.
package journal
