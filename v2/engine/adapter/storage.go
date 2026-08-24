package adapter

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

// Storage is the domain port implemented by storage adapters (e.g. StorageAdapter over journal.Store).
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
	Stats() StorageStats
	// Close releases all resources held by the storage engine.
	Close() error
}

// StatsProvider is an optional interface a store may implement to report statistics.
type StatsProvider interface {
	Stats() StorageStats
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

// StorageAdapter adapts an event-driven journal.Store into a domain Storage engine, managing
// SiblingSet serialization, vector clock reconciliation, and TTL filtering.
type StorageAdapter struct {
	store  *journal.Store
	nodeID node.NodeID

	nextReqID uint64
	slots     *pool.SlotTable[pendingStorageOp]

	// OnStorageErrorHook is invoked whenever the underlying store reports a storage error.
	OnStorageErrorHook func(err error)
}

var _ Storage = (*StorageAdapter)(nil)
var _ journal.StoreHandler = (*StorageAdapter)(nil)

// NewStorageAdapter creates a new Storage adapter over an event-driven journal.Store.
func NewStorageAdapter(store *journal.Store, nodeID node.NodeID) *StorageAdapter {
	a := &StorageAdapter{
		store:  store,
		nodeID: nodeID,
		slots:  pool.NewSlotTable[pendingStorageOp](1024),
	}
	store.SetHandler(a)
	return a
}

// OnStorageError implements journal.StoreHandler.
func (a *StorageAdapter) OnStorageError(err error) {
	if a.OnStorageErrorHook != nil {
		a.OnStorageErrorHook(err)
	}
}

// OnReadComplete implements journal.StoreHandler.
func (a *StorageAdapter) OnReadComplete(reqID uint64, key []byte, val []byte, err error) {
	slot, ok := a.slots.Get(reqID)
	if !ok || slot.Value.opType != storageOpRead {
		return
	}
	defer a.slots.Release(reqID)

	if err != nil {
		if slot.Value.onRead != nil {
			slot.Value.onRead(nil, err)
		}
		return
	}
	var ss SiblingSet
	if err := ss.UnmarshalBinary(val); err != nil {
		if slot.Value.onRead != nil {
			slot.Value.onRead(nil, fmt.Errorf("%w: decoding sibling set: %v", quorumerr.ErrCorruptedData, err))
		}
		return
	}
	if !slot.Value.rawOnly && lastSiblingIsTombstone(&ss) {
		if slot.Value.onRead != nil {
			slot.Value.onRead(nil, quorumerr.ErrKeyNotFound)
		}
		return
	}
	filtered := filterSiblings(&ss, !slot.Value.rawOnly)
	if len(filtered.Siblings) == 0 {
		if slot.Value.onRead != nil {
			slot.Value.onRead(nil, quorumerr.ErrKeyNotFound)
		}
		return
	}
	if slot.Value.onRead != nil {
		slot.Value.onRead(filtered, nil)
	}
}

// OnWriteComplete implements journal.StoreHandler.
func (a *StorageAdapter) OnWriteComplete(reqID uint64, key []byte, err error) {
	slot, ok := a.slots.Get(reqID)
	if !ok || slot.Value.opType != storageOpWrite {
		return
	}
	defer a.slots.Release(reqID)

	if slot.Value.onWrite != nil {
		slot.Value.onWrite(err)
	}
}

// OnScanComplete implements journal.StoreHandler.
func (a *StorageAdapter) OnScanComplete(scanID uint64, items []journal.ScanEntry, err error) {
	slot, ok := a.slots.Get(scanID)
	if !ok || slot.Value.opType != storageOpScan {
		return
	}
	defer a.slots.Release(scanID)

	if err != nil {
		if slot.Value.onScanDone != nil {
			slot.Value.onScanDone(err)
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
		if slot.Value.onScanFn != nil && !slot.Value.onScanFn(item.Key, filtered) {
			break
		}
	}
	if slot.Value.onScanDone != nil {
		slot.Value.onScanDone(nil)
	}
}

// OnCompactComplete implements journal.StoreHandler.
func (a *StorageAdapter) OnCompactComplete(compactID uint64, stats journal.CompactStats, err error) {
	slot, ok := a.slots.Get(compactID)
	if !ok || slot.Value.opType != storageOpCompact {
		return
	}
	defer a.slots.Release(compactID)

	if slot.Value.onCompact != nil {
		slot.Value.onCompact(stats, err)
	}
}

// Get returns the sibling set for key, filtering out tombstones and expired siblings.
func (a *StorageAdapter) Get(key []byte, done func(*SiblingSet, error)) {
	a.nextReqID++
	reqID := a.nextReqID
	slot := a.slots.Acquire(reqID)
	slot.Value = pendingStorageOp{
		opType:  storageOpRead,
		rawOnly: false,
		onRead:  done,
	}

	if err := a.store.Get(reqID, key); err != nil {
		a.slots.Release(reqID)
		done(nil, err)
	}
}

// GetRaw returns the sibling set for key with tombstones visible (used by anti-entropy).
func (a *StorageAdapter) GetRaw(key []byte, done func(*SiblingSet, error)) {
	a.nextReqID++
	reqID := a.nextReqID
	slot := a.slots.Acquire(reqID)
	slot.Value = pendingStorageOp{
		opType:  storageOpRead,
		rawOnly: true,
		onRead:  done,
	}

	if err := a.store.Get(reqID, key); err != nil {
		a.slots.Release(reqID)
		done(nil, err)
	}
}

// Put reconciles incoming siblings against existing siblings and persists the result.
func (a *StorageAdapter) Put(key []byte, siblings *SiblingSet, done func(error)) {
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

		if err := a.store.Put(reqID, key, buf); err != nil {
			a.slots.Release(reqID)
			done(err)
		}
	})
}

// Delete writes a tombstone sibling causally ordered by ctx.
func (a *StorageAdapter) Delete(key []byte, ctx vclock.VectorClock, done func(error)) {
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
func (a *StorageAdapter) Scan(start, end []byte, fn ScanFunc, done func(error)) {
	a.nextReqID++
	scanID := a.nextReqID
	slot := a.slots.Acquire(scanID)
	slot.Value = pendingStorageOp{
		opType:     storageOpScan,
		onScanFn:   fn,
		onScanDone: done,
	}

	if err := a.store.Scan(scanID, start, end); err != nil {
		a.slots.Release(scanID)
		done(err)
	}
}

// Compact initiates compaction on the underlying journal.Store, applying domain-level
// tombstone and TTL filtering.
func (a *StorageAdapter) Compact(done func(journal.CompactStats, error)) {
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

	if err := a.store.Compact(compactID, filter); err != nil {
		a.slots.Release(compactID)
		done(journal.CompactStats{}, err)
	}
}

// LocalNodeID returns the configured local node identifier.
func (a *StorageAdapter) LocalNodeID() node.NodeID {
	return a.nodeID
}

// Stats returns point-in-time statistics from the underlying store if supported.
func (a *StorageAdapter) Stats() StorageStats {
	if sp, ok := any(a.store).(StatsProvider); ok {
		return sp.Stats()
	}
	return StorageStats{}
}

// Close closes the underlying store.
func (a *StorageAdapter) Close() error {
	a.slots.Reset()
	return a.store.Close()
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
