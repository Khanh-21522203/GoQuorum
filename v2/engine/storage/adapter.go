package storage

import (
	"errors"
	"fmt"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/infra/storage/journal"
)

// StatsProvider is an optional interface a KVStore may implement to report statistics.
type StatsProvider interface {
	Stats() Stats
}

type pendingRead struct {
	key     []byte
	rawOnly bool
	cb      func(*SiblingSet, error)
}

type pendingScan struct {
	fn   ScanFunc
	done func(error)
}

// Adapter adapts an event-driven KVStore into a domain Storage engine, managing
// SiblingSet serialization, vector clock reconciliation, and TTL filtering.
type Adapter struct {
	kv     KVStore
	nodeID node.NodeID

	nextReqID     uint64
	pendingReads  map[uint64]pendingRead
	pendingWrites map[uint64]func(error)
	pendingScans  map[uint64]*pendingScan

	// OnStorageError is invoked whenever the underlying KVStore reports a storage error.
	OnStorageError func(err error)
}

var _ Storage = (*Adapter)(nil)

// NewAdapter creates a new Storage adapter over an event-driven KVStore.
func NewAdapter(kv KVStore, nodeID node.NodeID) *Adapter {
	a := &Adapter{
		kv:            kv,
		nodeID:        nodeID,
		pendingReads:  make(map[uint64]pendingRead),
		pendingWrites: make(map[uint64]func(error)),
		pendingScans:  make(map[uint64]*pendingScan),
	}
	kv.SetOnReadComplete(a.onReadComplete)
	kv.SetOnWriteComplete(a.onWriteComplete)
	kv.SetOnScanComplete(a.onScanComplete)
	kv.SetOnStorageError(a.onStorageError)
	return a
}

func (a *Adapter) onStorageError(err error) {
	if a.OnStorageError != nil {
		a.OnStorageError(err)
	}
}

func (a *Adapter) onReadComplete(reqID uint64, key []byte, val []byte, err error) {
	pr, ok := a.pendingReads[reqID]
	if !ok {
		return
	}
	delete(a.pendingReads, reqID)

	if err != nil {
		pr.cb(nil, err)
		return
	}
	var ss SiblingSet
	if err := ss.UnmarshalBinary(val); err != nil {
		pr.cb(nil, fmt.Errorf("%w: decoding sibling set: %v", quorumerr.ErrCorruptedData, err))
		return
	}
	if !pr.rawOnly && lastSiblingIsTombstone(&ss) {
		pr.cb(nil, quorumerr.ErrKeyNotFound)
		return
	}
	filtered := filterSiblings(&ss, !pr.rawOnly)
	if len(filtered.Siblings) == 0 {
		pr.cb(nil, quorumerr.ErrKeyNotFound)
		return
	}
	pr.cb(filtered, nil)
}

func (a *Adapter) onWriteComplete(reqID uint64, key []byte, err error) {
	cb, ok := a.pendingWrites[reqID]
	if !ok {
		return
	}
	delete(a.pendingWrites, reqID)
	cb(err)
}

func (a *Adapter) onScanComplete(scanID uint64, items []journal.ScanEntry, err error) {
	ps, ok := a.pendingScans[scanID]
	if !ok {
		return
	}
	delete(a.pendingScans, scanID)

	if err != nil {
		ps.done(err)
		return
	}

	for _, item := range items {
		var ss SiblingSet
		if err := ss.UnmarshalBinary(item.Value); err != nil {
			continue
		}
		if lastSiblingIsTombstone(&ss) {
			continue
		}
		filtered := filterSiblings(&ss, true)
		if len(filtered.Siblings) == 0 {
			continue
		}
		if !ps.fn(item.Key, filtered) {
			break // Early stop requested by caller
		}
	}
	ps.done(nil)
}

// Get returns active siblings for key, filtering out tombstones and expired values.
func (a *Adapter) Get(key []byte, done func(*SiblingSet, error)) {
	a.nextReqID++
	reqID := a.nextReqID
	a.pendingReads[reqID] = pendingRead{key: key, rawOnly: false, cb: done}

	if err := a.kv.Get(reqID, key); err != nil {
		delete(a.pendingReads, reqID)
		done(nil, err)
	}
}

// GetRaw returns all siblings for key including tombstones (used by anti-entropy and read-repair).
func (a *Adapter) GetRaw(key []byte, done func(*SiblingSet, error)) {
	a.nextReqID++
	reqID := a.nextReqID
	a.pendingReads[reqID] = pendingRead{key: key, rawOnly: true, cb: done}

	if err := a.kv.Get(reqID, key); err != nil {
		delete(a.pendingReads, reqID)
		done(nil, err)
	}
}

// Put reconciles incoming siblings with the existing set using append-union policy.
func (a *Adapter) Put(key []byte, incoming *SiblingSet, done func(error)) {
	a.GetRaw(key, func(existing *SiblingSet, err error) {
		if err != nil && !errors.Is(err, quorumerr.ErrKeyNotFound) {
			done(err)
			return
		}
		var merged SiblingSet
		if existing != nil {
			merged.Siblings = append(append([]Sibling{}, existing.Siblings...), incoming.Siblings...)
		} else {
			merged.Siblings = append([]Sibling{}, incoming.Siblings...)
		}
		buf, mErr := merged.MarshalBinary()
		if mErr != nil {
			done(fmt.Errorf("storage: encoding sibling set: %w", mErr))
			return
		}
		a.nextReqID++
		reqID := a.nextReqID
		a.pendingWrites[reqID] = done
		if pErr := a.kv.Put(reqID, key, buf); pErr != nil {
			delete(a.pendingWrites, reqID)
			done(pErr)
		}
	})
}

// Delete appends a deletion tombstone for key causally ordered by ctx.
func (a *Adapter) Delete(key []byte, ctx vclock.VectorClock, done func(error)) {
	tombstone := &SiblingSet{Siblings: []Sibling{{
		Tombstone: true,
		VClock:    ctx,
		Timestamp: time.Now().Unix(),
	}}}
	a.Put(key, tombstone, done)
}

// Scan visits every key in [start, end) in order with tombstones filtered out.
func (a *Adapter) Scan(start, end []byte, fn ScanFunc, done func(error)) {
	a.nextReqID++
	scanID := a.nextReqID
	a.pendingScans[scanID] = &pendingScan{fn: fn, done: done}

	if err := a.kv.Scan(scanID, start, end); err != nil {
		delete(a.pendingScans, scanID)
		done(err)
	}
}

// LocalNodeID returns the configured local node identifier.
func (a *Adapter) LocalNodeID() node.NodeID {
	return a.nodeID
}

// Stats returns point-in-time statistics from the underlying KVStore if supported.
func (a *Adapter) Stats() Stats {
	if sp, ok := a.kv.(StatsProvider); ok {
		return sp.Stats()
	}
	return Stats{}
}

// Close closes the underlying KVStore.
func (a *Adapter) Close() error {
	return a.kv.Close()
}

func filterSiblings(ss *SiblingSet, dropTombstones bool) *SiblingSet {
	now := time.Now().Unix()
	kept := make([]Sibling, 0, len(ss.Siblings))
	for _, sib := range ss.Siblings {
		if sib.ExpiresAt != 0 && sib.ExpiresAt <= now {
			continue
		}
		if dropTombstones && sib.Tombstone {
			continue
		}
		kept = append(kept, sib)
	}
	return &SiblingSet{Siblings: kept}
}

func lastSiblingIsTombstone(ss *SiblingSet) bool {
	if ss == nil || len(ss.Siblings) == 0 {
		return false
	}
	return ss.Siblings[len(ss.Siblings)-1].Tombstone
}
