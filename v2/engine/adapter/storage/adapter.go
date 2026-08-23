package storage

import (
	"errors"
	"fmt"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/storage/journal"
)

// StatsProvider is an optional interface a KVStore may implement to report statistics.
type StatsProvider interface {
	Stats() Stats
}

type storageOpType uint8

const (
	storageOpRead storageOpType = iota + 1
	storageOpWrite
	storageOpScan
	storageOpCompact
)

type pendingStorageOp struct {
	opType     storageOpType
	rawOnly    bool
	onRead     func(*SiblingSet, error)
	onWrite    func(error)
	onScanFn   ScanFunc
	onScanDone func(error)
	onCompact  func(journal.CompactStats, error)
}

// Adapter adapts an event-driven KVStore into a domain Storage engine, managing
// SiblingSet serialization, vector clock reconciliation, and TTL filtering.
type Adapter struct {
	kv     KVStore
	nodeID node.NodeID

	nextReqID uint64
	slots     *pool.SlotTable[pendingStorageOp]

	// OnStorageError is invoked whenever the underlying KVStore reports a storage error.
	OnStorageError func(err error)
}

var _ Storage = (*Adapter)(nil)

// NewAdapter creates a new Storage adapter over an event-driven KVStore.
func NewAdapter(kv KVStore, nodeID node.NodeID) *Adapter {
	a := &Adapter{
		kv:     kv,
		nodeID: nodeID,
		slots:  pool.NewSlotTable[pendingStorageOp](1024),
	}
	kv.SetOnReadComplete(a.onReadComplete)
	kv.SetOnWriteComplete(a.onWriteComplete)
	kv.SetOnScanComplete(a.onScanComplete)
	kv.SetOnCompactComplete(a.onCompactComplete)
	kv.SetOnStorageError(a.onStorageError)
	return a
}

func (a *Adapter) onStorageError(err error) {
	if a.OnStorageError != nil {
		a.OnStorageError(err)
	}
}

func (a *Adapter) onReadComplete(reqID uint64, key []byte, val []byte, err error) {
	slot, ok := a.slots.Get(reqID)
	if !ok || slot.Value.opType != storageOpRead {
		return
	}
	cb := slot.Value.onRead
	rawOnly := slot.Value.rawOnly
	a.slots.Release(reqID)

	if err != nil {
		if cb != nil {
			cb(nil, err)
		}
		return
	}
	var ss SiblingSet
	if err := ss.UnmarshalBinary(val); err != nil {
		if cb != nil {
			cb(nil, fmt.Errorf("%w: decoding sibling set: %v", quorumerr.ErrCorruptedData, err))
		}
		return
	}
	if !rawOnly && lastSiblingIsTombstone(&ss) {
		if cb != nil {
			cb(nil, quorumerr.ErrKeyNotFound)
		}
		return
	}
	filtered := filterSiblings(&ss, !rawOnly)
	if len(filtered.Siblings) == 0 {
		if cb != nil {
			cb(nil, quorumerr.ErrKeyNotFound)
		}
		return
	}
	if cb != nil {
		cb(filtered, nil)
	}
}

func (a *Adapter) onWriteComplete(reqID uint64, key []byte, err error) {
	slot, ok := a.slots.Get(reqID)
	if !ok || slot.Value.opType != storageOpWrite {
		return
	}
	cb := slot.Value.onWrite
	a.slots.Release(reqID)
	if cb != nil {
		cb(err)
	}
}

func (a *Adapter) onScanComplete(scanID uint64, items []journal.ScanEntry, err error) {
	slot, ok := a.slots.Get(scanID)
	if !ok || slot.Value.opType != storageOpScan {
		return
	}
	scanFn := slot.Value.onScanFn
	done := slot.Value.onScanDone
	a.slots.Release(scanID)

	if err != nil {
		if done != nil {
			done(err)
		}
		return
	}

	for _, item := range items {
		var ss SiblingSet
		if err := ss.UnmarshalBinary(item.Value); err != nil {
			continue // Skip corrupted entry
		}
		if lastSiblingIsTombstone(&ss) {
			continue // Skip tombstone in standard domain scan
		}
		filtered := filterSiblings(&ss, true)
		if len(filtered.Siblings) == 0 {
			continue
		}
		if scanFn != nil && !scanFn(item.Key, filtered) {
			break
		}
	}
	if done != nil {
		done(nil)
	}
}

func (a *Adapter) onCompactComplete(compactID uint64, stats journal.CompactStats, err error) {
	slot, ok := a.slots.Get(compactID)
	if !ok || slot.Value.opType != storageOpCompact {
		return
	}
	cb := slot.Value.onCompact
	a.slots.Release(compactID)
	if cb != nil {
		cb(stats, err)
	}
}

// Get returns the sibling set for key, filtering out tombstones and expired siblings.
func (a *Adapter) Get(key []byte, done func(*SiblingSet, error)) {
	a.nextReqID++
	reqID := a.nextReqID
	slot := a.slots.Acquire(reqID)
	slot.Value = pendingStorageOp{
		opType:  storageOpRead,
		rawOnly: false,
		onRead:  done,
	}

	if err := a.kv.Get(reqID, key); err != nil {
		a.slots.Release(reqID)
		done(nil, err)
	}
}

// GetRaw returns the sibling set for key with tombstones visible (used by anti-entropy).
func (a *Adapter) GetRaw(key []byte, done func(*SiblingSet, error)) {
	a.nextReqID++
	reqID := a.nextReqID
	slot := a.slots.Acquire(reqID)
	slot.Value = pendingStorageOp{
		opType:  storageOpRead,
		rawOnly: true,
		onRead:  done,
	}

	if err := a.kv.Get(reqID, key); err != nil {
		a.slots.Release(reqID)
		done(nil, err)
	}
}

// Put reconciles incoming siblings against existing siblings and persists the result.
func (a *Adapter) Put(key []byte, siblings *SiblingSet, done func(error)) {
	a.GetRaw(key, func(existing *SiblingSet, err error) {
		if err != nil && !errors.Is(err, quorumerr.ErrKeyNotFound) {
			done(err)
			return
		}

		var merged *SiblingSet
		if existing == nil {
			merged = siblings
		} else {
			merged = Reconcile(existing, siblings)
		}

		buf, err := merged.MarshalBinary()
		if err != nil {
			done(fmt.Errorf("%w: encoding sibling set: %v", quorumerr.ErrCorruptedData, err))
			return
		}

		a.nextReqID++
		reqID := a.nextReqID
		slot := a.slots.Acquire(reqID)
		slot.Value = pendingStorageOp{
			opType:  storageOpWrite,
			onWrite: done,
		}

		if err := a.kv.Put(reqID, key, buf); err != nil {
			a.slots.Release(reqID)
			done(err)
		}
	})
}

// Delete writes a tombstone sibling causally ordered by ctx.
func (a *Adapter) Delete(key []byte, ctx vclock.VectorClock, done func(error)) {
	tombstone := &SiblingSet{
		Siblings: []Sibling{
			{
				VClock:    ctx.Copy(),
				Timestamp: time.Now().UnixNano(),
				Tombstone: true,
			},
		},
	}
	a.Put(key, tombstone, done)
}

// Scan visits every key in [start, end) in order, invoking fn for each one.
func (a *Adapter) Scan(start, end []byte, fn ScanFunc, done func(error)) {
	a.nextReqID++
	scanID := a.nextReqID
	slot := a.slots.Acquire(scanID)
	slot.Value = pendingStorageOp{
		opType:     storageOpScan,
		onScanFn:   fn,
		onScanDone: done,
	}

	if err := a.kv.Scan(scanID, start, end); err != nil {
		a.slots.Release(scanID)
		done(err)
	}
}

// Compact initiates compaction on the underlying KVStore, applying domain-level
// tombstone and TTL filtering.
func (a *Adapter) Compact(done func(journal.CompactStats, error)) {
	a.nextReqID++
	compactID := a.nextReqID
	slot := a.slots.Acquire(compactID)
	slot.Value = pendingStorageOp{
		opType:    storageOpCompact,
		onCompact: done,
	}

	filter := func(key, val []byte) (bool, []byte) {
		var ss SiblingSet
		if err := ss.UnmarshalBinary(val); err != nil {
			return false, nil // Discard corrupted data
		}
		// If last sibling is tombstone, drop record entirely during compaction
		if lastSiblingIsTombstone(&ss) {
			return false, nil
		}
		filtered := filterSiblings(&ss, true)
		if len(filtered.Siblings) == 0 {
			return false, nil
		}
		newBytes, err := filtered.MarshalBinary()
		if err != nil {
			return false, nil
		}
		return true, newBytes
	}

	if err := a.kv.Compact(compactID, filter); err != nil {
		a.slots.Release(compactID)
		done(journal.CompactStats{}, err)
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
	a.slots.Reset()
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
