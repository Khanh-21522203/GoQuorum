package journal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/infra/ioruntime"
)

// Options configures Open.
type Options struct {
	// Path is the WAL file's location on disk. It is created if absent.
	Path string
	// NodeID is the ID of the node this storage engine serves, returned
	// verbatim by LocalNodeID.
	NodeID node.NodeID
}

// Store implements engine/storage.Storage as an append-only WAL with an
// in-memory index. See the package doc for the on-disk format, the
// HandleCompletion ownership contract, and the reconciliation policy.
type Store struct {
	f    *os.File
	fd   int
	rt   *ioruntime.Runtime
	opts Options

	idx *index

	// writeOffset is the next byte offset a write will be reserved at. See
	// doc.go's "Write offset allocation" section for why it advances at
	// submit time rather than at completion time.
	writeOffset int64

	nextUserData uint64
	pending      map[uint64]func(reactor.Event)

	// OnStorageError is invoked whenever an underlying io_uring submit or completion reports an error.
	OnStorageError func(err error)
}

var _ storage.Storage = (*Store)(nil)

// Open opens (creating if necessary) the WAL file at opts.Path, replays it
// synchronously to rebuild the index and locate the write tail, and
// returns a Store ready to submit io_uring operations through rt. See the
// package doc's "Ownership contract" section for how to wire the returned
// Store's HandleCompletion method up to a reactor.Reactor driving rt.
func Open(rt *ioruntime.Runtime, opts Options) (*Store, error) {
	f, err := os.OpenFile(opts.Path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: opening WAL file %q: %v", quorumerr.ErrStorageIO, opts.Path, err)
	}

	idx, tail, err := Replay(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	return &Store{
		f:           f,
		fd:          int(f.Fd()),
		rt:          rt,
		opts:        opts,
		idx:         idx,
		writeOffset: tail,
		pending:     make(map[uint64]func(reactor.Event)),
	}, nil
}

// HandleCompletion dispatches a reactor.Event back to whichever Store
// method submitted the io_uring operation it completes. The Store's owner
// must register this as its reactor's event handler; see the package doc.
func (s *Store) HandleCompletion(ev reactor.Event) {
	cb, ok := s.pending[ev.UserData]
	if !ok {
		// Stale or foreign completion; nothing this Store submitted.
		return
	}
	delete(s.pending, ev.UserData)
	if ev.Err != nil && s.OnStorageError != nil {
		s.OnStorageError(ev.Err)
	}
	cb(ev)
}

// register allocates a fresh user-data value, associates cb with it, and
// returns it for use in a Submit* call.
func (s *Store) register(cb func(reactor.Event)) uint64 {
	s.nextUserData++
	ud := s.nextUserData
	s.pending[ud] = cb
	return ud
}

// unregister cancels a previously registered callback, used when
// submitting the operation itself failed synchronously (so no completion
// will ever arrive for it).
func (s *Store) unregister(ud uint64) {
	delete(s.pending, ud)
}

// Get returns the sibling set for key, filtering out tombstones and
// expired siblings.
func (s *Store) Get(key []byte, done func(*storage.SiblingSet, error)) {
	entry, ok := s.idx.Get(key)
	if !ok || entry.Tombstone {
		done(nil, quorumerr.ErrKeyNotFound)
		return
	}
	s.readRecord(entry, func(_ []byte, siblings *storage.SiblingSet, err error) {
		if err != nil {
			done(nil, err)
			return
		}
		done(filterSiblings(siblings, true), nil)
	})
}

// GetRaw returns the sibling set for key with tombstones visible (only
// TTL-expired siblings are filtered). Unlike Get, a tombstoned key is not
// treated as absent here: read-repair/anti-entropy needs to see the
// tombstone record itself, per the interface's doc comment.
func (s *Store) GetRaw(key []byte, done func(*storage.SiblingSet, error)) {
	entry, ok := s.idx.Get(key)
	if !ok {
		done(nil, quorumerr.ErrKeyNotFound)
		return
	}
	s.readRecord(entry, func(_ []byte, siblings *storage.SiblingSet, err error) {
		if err != nil {
			done(nil, err)
			return
		}
		done(filterSiblings(siblings, false), nil)
	})
}

// Put reconciles siblings into the store for key. See doc.go's
// "Put/Delete reconciliation policy" section for the exact append-union
// policy implemented here.
func (s *Store) Put(key []byte, siblings *storage.SiblingSet, done func(error)) {
	s.reconcileAndWrite(key, siblings, done)
}

// Delete writes a tombstone for key, causally ordered by ctx, by routing a
// single tombstone Sibling through the same reconciliation path as Put.
func (s *Store) Delete(key []byte, ctx vclock.VectorClock, done func(error)) {
	tombstone := &storage.SiblingSet{Siblings: []storage.Sibling{{
		Tombstone: true,
		VClock:    ctx,
		Timestamp: time.Now().Unix(),
	}}}
	s.reconcileAndWrite(key, tombstone, done)
}

// reconcileAndWrite implements the shared Put/Delete path: read back
// whatever is currently stored for key (GetRaw semantics: TTL-expired
// siblings dropped, tombstones visible), append incoming's siblings to it,
// and write the merged set as one new record.
func (s *Store) reconcileAndWrite(key []byte, incoming *storage.SiblingSet, done func(error)) {
	entry, ok := s.idx.Get(key)
	if !ok {
		s.writeRecord(key, incoming, done)
		return
	}
	s.readRecord(entry, func(_ []byte, existing *storage.SiblingSet, err error) {
		if err != nil {
			done(fmt.Errorf("%w: reading existing record for %q to reconcile: %v", quorumerr.ErrStorageIO, key, err))
			return
		}
		existing = filterSiblings(existing, false)
		merged := &storage.SiblingSet{
			Siblings: append(append([]storage.Sibling{}, existing.Siblings...), incoming.Siblings...),
		}
		s.writeRecord(key, merged, done)
	})
}

// writeRecord encodes key/siblings and submits an async positioned write
// at a freshly reserved tail offset, updating the index only once the
// write's completion reports success.
func (s *Store) writeRecord(key []byte, siblings *storage.SiblingSet, done func(error)) {
	buf, err := EncodeRecord(key, siblings)
	if err != nil {
		done(fmt.Errorf("%w: encoding record for %q: %v", quorumerr.ErrStorageIO, key, err))
		return
	}

	offset := s.writeOffset
	s.writeOffset += int64(len(buf)) // reserved immediately; see doc.go.

	keyCopy := append([]byte(nil), key...)
	tombstone := lastSiblingIsTombstone(siblings)

	ud := s.register(func(ev reactor.Event) {
		if ev.Err != nil {
			done(fmt.Errorf("%w: writing record for %q: %v", quorumerr.ErrStorageIO, keyCopy, ev.Err))
			return
		}
		if int(ev.Result) != len(buf) {
			done(fmt.Errorf("%w: short write for %q: wrote %d of %d bytes",
				quorumerr.ErrStorageIO, keyCopy, ev.Result, len(buf)))
			return
		}
		s.idx.Set(keyCopy, indexEntry{Offset: offset, Length: uint32(len(buf)), Tombstone: tombstone})
		done(nil)
	})
	if err := s.rt.SubmitPwrite(s.fd, buf, uint64(offset), ud); err != nil {
		s.unregister(ud)
		if s.OnStorageError != nil {
			s.OnStorageError(err)
		}
		done(fmt.Errorf("%w: submitting write for %q: %v", quorumerr.ErrStorageIO, keyCopy, err))
	}
}

// readRecord submits an async positioned read for the record described by
// entry and decodes it on completion.
func (s *Store) readRecord(entry indexEntry, done func(key []byte, siblings *storage.SiblingSet, err error)) {
	buf := make([]byte, entry.Length)
	ud := s.register(func(ev reactor.Event) {
		if ev.Err != nil {
			done(nil, nil, fmt.Errorf("%w: reading record: %v", quorumerr.ErrStorageIO, ev.Err))
			return
		}
		if int(ev.Result) != len(buf) {
			done(nil, nil, fmt.Errorf("%w: short read: got %d of %d bytes", quorumerr.ErrStorageIO, ev.Result, len(buf)))
			return
		}
		key, siblings, _, err := DecodeRecord(buf)
		if err != nil {
			done(nil, nil, err)
			return
		}
		done(key, siblings, nil)
	})
	if err := s.rt.SubmitPread(s.fd, buf, uint64(entry.Offset), ud); err != nil {
		s.unregister(ud)
		if s.OnStorageError != nil {
			s.OnStorageError(err)
		}
		done(nil, nil, fmt.Errorf("%w: submitting read: %v", quorumerr.ErrStorageIO, err))
	}
}

// Scan visits every key in [start, end) in order, invoking fn for each
// one, reading records one at a time (waiting for each read's completion
// before issuing the next) since Store correlates at most one pending
// operation per submission and this is simplest given a single-threaded
// reactor. Keys tombstoned by the time Scan reaches them are skipped, the
// same as Get.
func (s *Store) Scan(start, end []byte, fn storage.ScanFunc, done func(error)) {
	keys := s.idx.Keys()

	lo := 0
	if len(start) > 0 {
		lo = sort.Search(len(keys), func(i int) bool { return bytes.Compare(keys[i], start) >= 0 })
	}
	hi := len(keys)
	if len(end) > 0 {
		hi = sort.Search(len(keys), func(i int) bool { return bytes.Compare(keys[i], end) >= 0 })
	}
	keys = keys[lo:hi]

	var i int
	var step func()
	step = func() {
		for i < len(keys) {
			key := keys[i]
			i++
			entry, ok := s.idx.Get(key)
			if !ok || entry.Tombstone {
				continue
			}
			s.readRecord(entry, func(_ []byte, siblings *storage.SiblingSet, err error) {
				if err != nil {
					done(err)
					return
				}
				if !fn(key, filterSiblings(siblings, true)) {
					done(nil)
					return
				}
				step()
			})
			return
		}
		done(nil)
	}
	step()
}

// LocalNodeID returns the ID of the node this storage engine serves.
func (s *Store) LocalNodeID() node.NodeID {
	return s.opts.NodeID
}

// Stats returns point-in-time storage engine statistics. L0FileCount and
// CompactionCount are always zero: this design has no SSTable-style
// leveled files and no compaction to count, so those metrics do not apply.
// SizeBytes is left zero for the same reason WALBytesWritten exists
// instead: the WAL file's logical size already is WALBytesWritten.
func (s *Store) Stats() storage.Stats {
	return storage.Stats{
		KeyCount:        int64(s.idx.Len()),
		WALBytesWritten: uint64(s.writeOffset),
	}
}

// Close releases all resources held by the storage engine. Per the
// interface's doc comment this is called only after the owning reactor's
// Run has returned, so a plain blocking close is both correct and simpler
// than routing a one-shot shutdown operation through io_uring.
func (s *Store) Close() error {
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	if err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EBADF) {
		return fmt.Errorf("%w: closing WAL file: %v", quorumerr.ErrStorageIO, err)
	}
	return nil
}

// filterSiblings returns a copy of ss with TTL-expired siblings always
// removed, and tombstoned siblings additionally removed when
// dropTombstones is true.
func filterSiblings(ss *storage.SiblingSet, dropTombstones bool) *storage.SiblingSet {
	now := time.Now().Unix()
	kept := make([]storage.Sibling, 0, len(ss.Siblings))
	for _, sib := range ss.Siblings {
		if sib.ExpiresAt != 0 && sib.ExpiresAt <= now {
			continue
		}
		if dropTombstones && sib.Tombstone {
			continue
		}
		kept = append(kept, sib)
	}
	return &storage.SiblingSet{Siblings: kept}
}

// lastSiblingIsTombstone reports whether the last sibling in ss is a
// tombstone, used to derive a key's whole-record tombstone status from
// the most recently appended write. See doc.go's "Put/Delete
// reconciliation policy" section.
func lastSiblingIsTombstone(ss *storage.SiblingSet) bool {
	if len(ss.Siblings) == 0 {
		return false
	}
	return ss.Siblings[len(ss.Siblings)-1].Tombstone
}
