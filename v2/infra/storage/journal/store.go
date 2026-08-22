// Package journal implements an event-driven, append-only Key-Value WAL engine backed by io_uring.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────────┐
// │                        CIRCULAR RING BUFFER WAL ARCHITECTURE                            │
// │                                                                                         │
// │                                     [ wal_0.log ]                                       │
// │                                  (Epoch: 104) ◄── HEAD                                  │
// │                                     ▲           │                                       │
// │                                    /             \                                      │
// │                                   /               ▼                                     │
// │                       [ wal_3.log ]                 [ wal_1.log ]                       │
// │                       (Epoch: 103)                  (Epoch: 101) ◄── TAIL               │
// │                                   \               /                                     │
// │                                    \             ▼                                      │
// │                                      [ wal_2.log ]                                      │
// │                                       (Epoch: 102)                                      │
// │                                                                                         │
// │  • 16-Byte Header: [Magic: 4B "QUOR"][Epoch: 8B uint64][Status: 1B][Reserved: 3B]       │
// │  • Status-Guided Replay: Anchors to latest StatusCompacted checkpoint.                 │
// │  • Crash-Safe Rotation: Pre-truncates to 16 bytes before writing header.                │
// │  • 100% Zero-Alloc: In-place codecs, ArrayPool memory rental, Generic SlotTable[T].     │
// └─────────────────────────────────────────────────────────────────────────────────────────┘
package journal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
)

const (
	// DefaultNumSegments is the default number of segment files in the circular ring.
	DefaultNumSegments = 4
	// DefaultSegmentSize is the default max byte size per segment file (64 MB).
	DefaultSegmentSize = 64 * 1024 * 1024

	// MaxCoalesceGap is the maximum byte gap between two adjacent records to merge them into a single sequential Pread.
	MaxCoalesceGap = 64 * 1024 // 64 KB
	// MaxCoalesceChunkSize is the maximum total byte span of a single coalesced Pread.
	MaxCoalesceChunkSize = 2 * 1024 * 1024 // 2 MB

	// scanUserDataFlag is set on bit 62 of io_uring UserData to distinguish scan chunk reads from standard Get reads.
	scanUserDataFlag = uint64(1) << 62

	// defaultInFlightSlots is the default capacity of the contiguous in-flight slot table.
	defaultInFlightSlots = 4096
)

// Options configures Store initialization and segment ring topology.
type Options struct {
	DataDir     string // Directory containing ring segment files (e.g. wal_0.log, wal_1.log...)
	Path        string // Single file path (for backwards compatibility if DataDir is empty)
	NumSegments int    // Number of segment files in the circular ring (min: 2, default: 4)
	SegmentSize uint64 // Maximum byte capacity per segment file (default: 64 MB)
}

// Stats reports point-in-time storage statistics across the ring.
type Stats struct {
	KeyCount        int64
	ActiveSeg       int
	TailSeg         int
	CurrentEpoch    uint64
	WALBytesWritten uint64
}

// ScanEntry represents a raw key-value pair returned in a scan batch.
type ScanEntry struct {
	Key   []byte
	Value []byte
}

// CompactFilter is a callback provided by higher layers to inspect and optionally modify
// or discard records during compaction. Return keep=false to drop the record.
type CompactFilter func(key, val []byte) (keep bool, newVal []byte)

// CompactStats reports the result of a completed compaction run.
type CompactStats struct {
	OriginalBytes  uint64
	CompactedBytes uint64
	BytesReclaimed uint64
	LiveKeyCount   int64
}

type opType uint8

const (
	opNone opType = iota
	opRead
	opWrite
)

type inFlightOp struct {
	op      opType
	keyBuf  [64]byte
	keyLen  uint16
	keyHeap []byte
	segID   uint16
	offset  int64
	length  uint32
	buf     []byte
}

func (s *inFlightOp) key() []byte {
	if len(s.keyHeap) > 0 {
		return s.keyHeap
	}
	return s.keyBuf[:s.keyLen]
}

func (s *inFlightOp) setKey(k []byte) {
	s.keyLen = uint16(len(k))
	if len(k) <= len(s.keyBuf) {
		copy(s.keyBuf[:len(k)], k)
		s.keyHeap = nil
	} else {
		s.keyHeap = append(s.keyHeap[:0], k...)
	}
}

// scanItem represents a single matched record in the flat scan item array.
type scanItem struct {
	keyIndex int    // Destination slot in final sorted results []ScanEntry
	segID    uint16 // Ring segment index
	offset   int64  // Physical file offset in that segment
	length   uint32 // Record length in bytes
}

// scanChunk represents a contiguous physical disk read covering a range of scanItems [startItem, endItem) in a single segment.
type scanChunk struct {
	segID     uint16 // Ring segment index
	offset    int64  // Starting byte offset in that segment
	length    uint32 // Total byte length to read
	startItem int    // First item index in scanState.items
	endItem   int    // Exclusive end item index in scanState.items
	buf       []byte // Rented disk buffer
}

type inFlightScan struct {
	items        []scanItem
	chunks       []scanChunk
	results      []ScanEntry
	arena        *pool.ByteArena
	pendingCount int
	failedErr    error
}

// Store is an event-driven, append-only Key-Value WAL backed by a Circular Ring Buffer of segment files and io_uring.
type Store struct {
	opts    Options
	dataDir string

	files []*os.File // Permanently open segment files [0 .. NumSegments-1]
	fds   []int      // Raw file descriptors for zero-overhead io_uring submission

	rt  *ioruntime.Runtime
	idx *index

	currentEpoch uint64 // Monotonic epoch counter stamped on segment headers
	activeSeg    int    // HEAD: current active segment index for writes [0 .. NumSegments-1]
	writeOffset  int64  // Current write byte offset in activeSeg [SegmentHeaderSize .. SegmentSize)
	tailSeg      int    // TAIL: oldest uncompacted segment [0 .. NumSegments-1]

	slots         *pool.SlotTable[inFlightOp] // Reusable generic slot table for zero-alloc in-flight tracking
	inFlightScans map[uint64]*inFlightScan    // scanID -> scan state

	bytePool  *pool.BucketArrayPool[byte]      // Universal byte buffer pool (16B .. 2MB)
	itemPool  *pool.BucketArrayPool[scanItem]  // Pool for scan item slices
	chunkPool *pool.BucketArrayPool[scanChunk] // Pool for scan chunk slices
	scanPool  pool.ArrayPool[ScanEntry]        // Pool for final ScanEntry results

	// Event Hooks (Registered by higher layer)
	OnReadComplete    func(reqID uint64, key []byte, val []byte, err error)
	OnWriteComplete   func(reqID uint64, key []byte, err error)
	OnScanComplete    func(scanID uint64, items []ScanEntry, err error)
	OnCompactComplete func(compactID uint64, stats CompactStats, err error)
	OnStorageError    func(err error)
}

// Open initializes the Circular Ring Buffer WAL store, opens all segment files,
// replays records across the ring to build the in-memory index, and prepares the write head.
func Open(rt *ioruntime.Runtime, opts Options) (*Store, error) {
	if opts.NumSegments <= 0 {
		opts.NumSegments = DefaultNumSegments
	}
	if opts.NumSegments < 2 {
		opts.NumSegments = 2
	}
	if opts.SegmentSize == 0 {
		opts.SegmentSize = DefaultSegmentSize
	}

	dataDir := opts.DataDir
	if dataDir == "" && opts.Path != "" {
		dataDir = filepath.Dir(opts.Path)
	}
	if dataDir == "" {
		dataDir = "."
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: creating data directory %q: %v", quorumerr.ErrStorageIO, dataDir, err)
	}

	files := make([]*os.File, opts.NumSegments)
	fds := make([]int, opts.NumSegments)

	for i := 0; i < opts.NumSegments; i++ {
		segPath := filepath.Join(dataDir, fmt.Sprintf("wal_%d.log", i))
		if opts.Path != "" && opts.NumSegments == 1 {
			segPath = opts.Path
		}
		f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			for j := 0; j < i; j++ {
				_ = files[j].Close()
			}
			return nil, fmt.Errorf("%w: opening segment file %q: %v", quorumerr.ErrStorageIO, segPath, err)
		}
		files[i] = f
		fds[i] = int(f.Fd())
	}

	// Replay all segments in ring order using Status-Guided Log-Structured Replay
	idx, activeSeg, tailSeg, maxEpoch, tailOffset, err := ReplayRingSegments(files)
	if err != nil {
		for _, f := range files {
			_ = f.Close()
		}
		return nil, err
	}

	// If fresh uninitialized store, initialize epoch header on segment 0
	if maxEpoch == 0 {
		maxEpoch = 1
		_ = files[0].Truncate(SegmentHeaderSize)
		header := EncodeSegmentHeader(maxEpoch, StatusWriter)
		if _, err := files[0].WriteAt(header, 0); err != nil {
			for _, f := range files {
				_ = f.Close()
			}
			return nil, fmt.Errorf("%w: initializing segment 0 header: %v", quorumerr.ErrStorageIO, err)
		}
		tailOffset = SegmentHeaderSize
	} else if tailOffset < SegmentHeaderSize {
		tailOffset = SegmentHeaderSize
	}

	return &Store{
		opts:          opts,
		dataDir:       dataDir,
		files:         files,
		fds:           fds,
		rt:            rt,
		idx:           idx,
		currentEpoch:  maxEpoch,
		activeSeg:     activeSeg,
		writeOffset:   tailOffset,
		tailSeg:       tailSeg,
		slots:         pool.NewSlotTable[inFlightOp](defaultInFlightSlots),
		inFlightScans: make(map[uint64]*inFlightScan),
		bytePool:      pool.NewArrayPool[byte](64, 16, 64),
		itemPool:      pool.NewDefaultArrayPool[scanItem](),
		chunkPool:     pool.NewDefaultArrayPool[scanChunk](),
		scanPool:      pool.NewDefaultArrayPool[ScanEntry](),
	}, nil
}

// SetOnReadComplete registers the read completion event hook.
func (s *Store) SetOnReadComplete(fn func(reqID uint64, key []byte, val []byte, err error)) {
	s.OnReadComplete = fn
}

// SetOnWriteComplete registers the write completion event hook.
func (s *Store) SetOnWriteComplete(fn func(reqID uint64, key []byte, err error)) {
	s.OnWriteComplete = fn
}

// SetOnScanComplete registers the scan completion event hook.
func (s *Store) SetOnScanComplete(fn func(scanID uint64, items []ScanEntry, err error)) {
	s.OnScanComplete = fn
}

// SetOnCompactComplete registers the compaction completion event hook.
func (s *Store) SetOnCompactComplete(fn func(compactID uint64, stats CompactStats, err error)) {
	s.OnCompactComplete = fn
}

// SetOnStorageError registers the storage error event hook.
func (s *Store) SetOnStorageError(fn func(err error)) {
	s.OnStorageError = fn
}

// Get issues an asynchronous point read request for key tagged with reqID.
// 100% Zero-Alloc: Direct O(1) routing, rented read buffer, generic SlotTable tracking.
func (s *Store) Get(reqID uint64, key []byte) error {
	entry, ok := s.idx.Get(key)
	if !ok {
		if s.OnReadComplete != nil {
			s.OnReadComplete(reqID, key, nil, quorumerr.ErrKeyNotFound)
		}
		return nil
	}

	slot := s.slots.Acquire(reqID)
	slot.Value.op = opRead
	slot.Value.segID = entry.SegID
	slot.Value.offset = entry.Offset
	slot.Value.length = entry.Length
	slot.Value.setKey(key)

	buf := s.bytePool.Rent(int(entry.Length))
	slot.Value.buf = buf[:entry.Length]

	fd := s.fds[entry.SegID]
	if err := s.rt.SubmitPread(fd, slot.Value.buf, uint64(entry.Offset), reqID); err != nil {
		s.slots.Release(reqID)
		s.bytePool.Return(slot.Value.buf)
		if s.OnStorageError != nil {
			s.OnStorageError(err)
		}
		if s.OnReadComplete != nil {
			s.OnReadComplete(reqID, key, nil, fmt.Errorf("%w: submitting read: %v", quorumerr.ErrStorageIO, err))
		}
		return err
	}
	return nil
}

// Put issues an asynchronous write request for key-val tagged with reqID.
// 100% Zero-Alloc: In-place encoding, rented write buffer, generic SlotTable tracking.
func (s *Store) Put(reqID uint64, key []byte, val []byte) error {
	totalLen := RecordEncodedLen(len(key), len(val))
	recLen := int64(totalLen)

	// Check if active segment has reached capacity: rotate circular HEAD
	if s.writeOffset+recLen > int64(s.opts.SegmentSize) && s.writeOffset > SegmentHeaderSize {
		nextSeg := (s.activeSeg + 1) % s.opts.NumSegments
		s.currentEpoch++

		// Truncate FIRST to wipe all ghost records from past cycles
		_ = s.files[nextSeg].Truncate(SegmentHeaderSize)

		// Stamp new epoch header at offset 0 of rotated segment
		header := EncodeSegmentHeader(s.currentEpoch, StatusWriter)
		if _, err := s.files[nextSeg].WriteAt(header, 0); err != nil {
			if s.OnStorageError != nil {
				s.OnStorageError(err)
			}
		}

		s.activeSeg = nextSeg
		s.writeOffset = SegmentHeaderSize // Overwrite recycled segment starting right after 16-byte header
	}

	segID := uint16(s.activeSeg)
	offset := s.writeOffset
	s.writeOffset += recLen

	slot := s.slots.Acquire(reqID)
	slot.Value.op = opWrite
	slot.Value.segID = segID
	slot.Value.offset = offset
	slot.Value.length = uint32(totalLen)
	slot.Value.setKey(key)

	buf := s.bytePool.Rent(totalLen)
	encoded, err := EncodeRecordTo(buf, key, val)
	if err != nil {
		s.slots.Release(reqID)
		s.bytePool.Return(buf)
		return fmt.Errorf("%w: encoding record: %v", quorumerr.ErrStorageIO, err)
	}
	slot.Value.buf = encoded

	fd := s.fds[segID]
	if err := s.rt.SubmitPwrite(fd, slot.Value.buf, uint64(offset), reqID); err != nil {
		s.slots.Release(reqID)
		s.bytePool.Return(slot.Value.buf)
		if s.OnStorageError != nil {
			s.OnStorageError(err)
		}
		if s.OnWriteComplete != nil {
			s.OnWriteComplete(reqID, key, fmt.Errorf("%w: submitting write: %v", quorumerr.ErrStorageIO, err))
		}
		return err
	}
	return nil
}

// Delete issues an asynchronous deletion record write tagged with reqID.
func (s *Store) Delete(reqID uint64, key []byte) error {
	return s.Put(reqID, key, nil)
}

// Scan initiates an asynchronous range scan over [start, end) using the Contiguous Slot Table model.
// Groups items by segment file, applies Range Coalescing per segment, and dispatches parallel io_uring reads.
func (s *Store) Scan(scanID uint64, start, end []byte) error {
	keys := s.idx.Keys()

	lo := 0
	if len(start) > 0 {
		lo = sort.Search(len(keys), func(i int) bool { return bytes.Compare(keys[i], start) >= 0 })
	}
	hi := len(keys)
	if len(end) > 0 {
		hi = sort.Search(len(keys), func(i int) bool { return bytes.Compare(keys[i], end) >= 0 })
	}
	matched := keys[lo:hi]

	if len(matched) == 0 {
		if s.OnScanComplete != nil {
			s.OnScanComplete(scanID, nil, nil)
		}
		return nil
	}

	// 1. Rent flat items slice from itemPool
	items := s.itemPool.Rent(len(matched))
	items = items[:0]
	for i, k := range matched {
		entry, ok := s.idx.Get(k)
		if !ok {
			continue
		}
		items = append(items, scanItem{
			keyIndex: i,
			segID:    entry.SegID,
			offset:   entry.Offset,
			length:   entry.Length,
		})
	}

	if len(items) == 0 {
		s.itemPool.Return(items)
		if s.OnScanComplete != nil {
			s.OnScanComplete(scanID, nil, nil)
		}
		return nil
	}

	// 2. Sort items primarily by SegID, then by physical Offset (Forward-Sweep disk locality per segment)
	sort.Slice(items, func(i, j int) bool {
		if items[i].segID != items[j].segID {
			return items[i].segID < items[j].segID
		}
		return items[i].offset < items[j].offset
	})

	// 3. Coalesce items into chunks per segment using chunkPool
	chunks := s.coalesceScanItemsPooled(items)

	results := s.scanPool.Rent(len(matched))
	results = results[:len(matched)] // Pre-expand length to allow direct slot assignment

	arena := pool.NewByteArena(s.bytePool, pool.DefaultArenaChunkSize)

	scanState := &inFlightScan{
		items:        items,
		chunks:       chunks,
		results:      results,
		arena:        arena,
		pendingCount: len(chunks),
	}
	s.inFlightScans[scanID] = scanState

	// 4. Submit all coalesced chunk reads in parallel to io_uring across the segment files
	for chunkIdx := range scanState.chunks {
		chunk := &scanState.chunks[chunkIdx]
		buf := s.bytePool.Rent(int(chunk.length))
		chunk.buf = buf[:chunk.length]

		// Bit-pack: Flag (bit 62) | ScanID (bits 16..61) | ChunkIndex (bits 0..15)
		userData := scanUserDataFlag | ((scanID & 0x3FFFFFFFFFFF) << 16) | uint64(chunkIdx&0xFFFF)
		fd := s.fds[chunk.segID]

		if err := s.rt.SubmitPread(fd, chunk.buf, uint64(chunk.offset), userData); err != nil {
			scanState.pendingCount--
			if scanState.failedErr == nil {
				scanState.failedErr = fmt.Errorf("%w: submitting scan chunk %d: %v", quorumerr.ErrStorageIO, chunkIdx, err)
			}
			if s.OnStorageError != nil {
				s.OnStorageError(err)
			}
		}
	}

	if scanState.pendingCount == 0 {
		for i := range scanState.chunks {
			s.bytePool.Return(scanState.chunks[i].buf)
		}
		s.chunkPool.Return(scanState.chunks)
		s.itemPool.Return(scanState.items)
		scanState.arena.Release()
		delete(s.inFlightScans, scanID)
		if s.OnScanComplete != nil {
			s.OnScanComplete(scanID, nil, scanState.failedErr)
		}
		s.scanPool.Return(results)
	}

	return nil
}

// coalesceScanItemsPooled groups forward-sorted scan items into chunks per segment using chunkPool.
func (s *Store) coalesceScanItemsPooled(items []scanItem) []scanChunk {
	if len(items) == 0 {
		return nil
	}

	chunks := s.chunkPool.Rent(len(items))
	chunks = chunks[:0]

	curChunk := scanChunk{
		segID:     items[0].segID,
		offset:    items[0].offset,
		length:    items[0].length,
		startItem: 0,
		endItem:   1,
	}

	for i := 1; i < len(items); i++ {
		item := items[i]
		sameSeg := item.segID == curChunk.segID
		chunkEnd := curChunk.offset + int64(curChunk.length)
		gap := item.offset - chunkEnd
		newTotalLen := curChunk.length + uint32(gap) + item.length

		if sameSeg && gap >= 0 && gap <= MaxCoalesceGap && newTotalLen <= MaxCoalesceChunkSize {
			curChunk.length = newTotalLen
			curChunk.endItem = i + 1
		} else {
			chunks = append(chunks, curChunk)
			curChunk = scanChunk{
				segID:     item.segID,
				offset:    item.offset,
				length:    item.length,
				startItem: i,
				endItem:   i + 1,
			}
		}
	}
	chunks = append(chunks, curChunk)
	return chunks
}

// Compact migrates surviving records from inactive ring segments forward to the active write head,
// frees compacted segments for future recycling, and updates in-memory index locations.
func (s *Store) Compact(compactID uint64, filter CompactFilter) error {
	keys := s.idx.Keys()
	var originalBytes, compactedBytes, reclaimedBytes uint64

	for _, k := range keys {
		entry, ok := s.idx.Get(k)
		if !ok {
			continue
		}
		originalBytes += uint64(entry.Length)

		buf := s.bytePool.Rent(int(entry.Length))
		buf = buf[:entry.Length]
		f := s.files[entry.SegID]

		if _, err := f.ReadAt(buf, entry.Offset); err != nil {
			s.bytePool.Return(buf)
			err = fmt.Errorf("%w: reading record for compaction from seg %d: %v", quorumerr.ErrStorageIO, entry.SegID, err)
			if s.OnStorageError != nil {
				s.OnStorageError(err)
			}
			if s.OnCompactComplete != nil {
				s.OnCompactComplete(compactID, CompactStats{}, err)
			}
			return err
		}

		key, val, _, err := DecodeRecord(buf)
		if err != nil {
			s.bytePool.Return(buf)
			continue
		}

		// Apply higher-layer domain filter (e.g. drop tombstones, prune expired TTLs)
		keep := true
		newVal := val
		if filter != nil {
			keep, newVal = filter(key, val)
		}
		if !keep {
			s.idx.Delete(key)
			reclaimedBytes += uint64(entry.Length)
			s.bytePool.Return(buf)
			continue
		}

		totalLen := RecordEncodedLen(len(key), len(newVal))
		writeBuf := s.bytePool.Rent(totalLen)
		recBytes, err := EncodeRecordTo(writeBuf, key, newVal)
		if err != nil {
			s.bytePool.Return(buf)
			s.bytePool.Return(writeBuf)
			continue
		}

		recLen := int64(len(recBytes))
		// Check segment rotation for active compaction target
		if s.writeOffset+recLen > int64(s.opts.SegmentSize) && s.writeOffset > SegmentHeaderSize {
			nextSeg := (s.activeSeg + 1) % s.opts.NumSegments
			s.currentEpoch++

			// Truncate FIRST to wipe all ghost records from past cycles
			_ = s.files[nextSeg].Truncate(SegmentHeaderSize)

			// Stamp new epoch header with StatusCompacted
			header := EncodeSegmentHeader(s.currentEpoch, StatusCompacted)
			_, _ = s.files[nextSeg].WriteAt(header, 0)

			s.activeSeg = nextSeg
			s.writeOffset = SegmentHeaderSize
		}

		targetSeg := uint16(s.activeSeg)
		targetOffset := s.writeOffset
		s.writeOffset += recLen

		targetFile := s.files[targetSeg]
		if _, err := targetFile.WriteAt(recBytes, targetOffset); err != nil {
			s.bytePool.Return(buf)
			s.bytePool.Return(writeBuf)
			err = fmt.Errorf("%w: writing compacted record to seg %d: %v", quorumerr.ErrStorageIO, targetSeg, err)
			if s.OnStorageError != nil {
				s.OnStorageError(err)
			}
			if s.OnCompactComplete != nil {
				s.OnCompactComplete(compactID, CompactStats{}, err)
			}
			return err
		}

		s.idx.Set(key, indexEntry{
			SegID:  targetSeg,
			Offset: targetOffset,
			Length: uint32(len(recBytes)),
		})

		compactedBytes += uint64(len(recBytes))
		s.bytePool.Return(buf)
		s.bytePool.Return(writeBuf)
	}

	// Stamp current active segment as StatusCompacted baseline
	compactHeader := EncodeSegmentHeader(s.currentEpoch, StatusCompacted)
	_, _ = s.files[s.activeSeg].WriteAt(compactHeader, 0)

	// Advance tail to current active segment (all older segments are now clean)
	s.tailSeg = s.activeSeg

	if originalBytes > compactedBytes {
		reclaimedBytes = originalBytes - compactedBytes
	}

	stats := CompactStats{
		OriginalBytes:  originalBytes,
		CompactedBytes: compactedBytes,
		BytesReclaimed: reclaimedBytes,
		LiveKeyCount:   int64(s.idx.Len()),
	}

	if s.OnCompactComplete != nil {
		s.OnCompactComplete(compactID, stats, nil)
	}
	return nil
}

// HandleCompletion dispatches an io_uring completion event to the awaiting event hook.
// 100% Zero-Alloc: Direct SlotTable lookup, zero-copy record views, buffer return to ArrayPool.
func (s *Store) HandleCompletion(ev reactor.Event) bool {
	reqID := ev.UserData

	if ev.Err != nil && s.OnStorageError != nil {
		s.OnStorageError(ev.Err)
	}

	// 1. Check if it's a Scan chunk completion (distinguished by scanUserDataFlag on bit 62)
	if reqID&scanUserDataFlag != 0 {
		scanID := (reqID &^ scanUserDataFlag) >> 16
		chunkIdx := int(reqID & 0xFFFF)

		scanState, exists := s.inFlightScans[scanID]
		if exists && chunkIdx < len(scanState.chunks) {
			chunk := &scanState.chunks[chunkIdx]

			if ev.Err != nil && scanState.failedErr == nil {
				scanState.failedErr = fmt.Errorf("%w: reading scan chunk: %v", quorumerr.ErrStorageIO, ev.Err)
			} else if int(ev.Result) != len(chunk.buf) && scanState.failedErr == nil {
				scanState.failedErr = fmt.Errorf("%w: short read: got %d of %d bytes", quorumerr.ErrStorageIO, ev.Result, len(chunk.buf))
			} else if scanState.failedErr == nil {
				// Unpack all records belonging to this chunk's index range [startItem, endItem) using zero-copy views into arena
				for i := chunk.startItem; i < chunk.endItem; i++ {
					item := scanState.items[i]
					relOffset := uint32(item.offset - chunk.offset)
					recordBytes := chunk.buf[relOffset : relOffset+item.length]
					kView, vView, _, err := DecodeRecord(recordBytes)
					if err != nil && scanState.failedErr == nil {
						scanState.failedErr = err
						break
					} else if err == nil {
						scanState.results[item.keyIndex] = ScanEntry{
							Key:   scanState.arena.Alloc(kView),
							Value: scanState.arena.Alloc(vView),
						}
					}
				}
			}

			// Return chunk buffer to bytePool
			s.bytePool.Return(chunk.buf)
			chunk.buf = nil

			scanState.pendingCount--
			if scanState.pendingCount == 0 {
				// All chunks for this scan have finished!
				results := scanState.results
				failedErr := scanState.failedErr
				s.itemPool.Return(scanState.items)
				s.chunkPool.Return(scanState.chunks)
				delete(s.inFlightScans, scanID)

				if s.OnScanComplete != nil {
					if failedErr != nil {
						s.OnScanComplete(scanID, nil, failedErr)
					} else {
						s.OnScanComplete(scanID, results, nil)
					}
				}
				s.scanPool.Return(results)
				scanState.arena.Release()
			}
		}
		return true
	}

	// 2. Point operation (Get / Put / Delete) via generic SlotTable
	slot, ok := s.slots.Get(reqID)
	if !ok {
		return false
	}

	buf := slot.Value.buf
	slot.Value.buf = nil
	op := slot.Value.op
	key := slot.Value.key()
	s.slots.Release(reqID)

	defer func() {
		if buf != nil {
			s.bytePool.Return(buf)
		}
	}()

	switch op {
	case opRead:
		if ev.Err != nil {
			if s.OnReadComplete != nil {
				s.OnReadComplete(reqID, key, nil, fmt.Errorf("%w: reading record: %v", quorumerr.ErrStorageIO, ev.Err))
			}
			return true
		}
		if int(ev.Result) != len(buf) {
			if s.OnReadComplete != nil {
				s.OnReadComplete(reqID, key, nil, fmt.Errorf("%w: short read: got %d of %d bytes", quorumerr.ErrStorageIO, ev.Result, len(buf)))
			}
			return true
		}
		_, valView, _, err := DecodeRecord(buf)
		if s.OnReadComplete != nil {
			// Detach value for caller callback safety
			valCopy := append([]byte(nil), valView...)
			s.OnReadComplete(reqID, key, valCopy, err)
		}
		return true

	case opWrite:
		if ev.Err != nil {
			if s.OnWriteComplete != nil {
				s.OnWriteComplete(reqID, key, fmt.Errorf("%w: writing record: %v", quorumerr.ErrStorageIO, ev.Err))
			}
			return true
		}
		if int(ev.Result) != int(slot.Value.length) {
			if s.OnWriteComplete != nil {
				s.OnWriteComplete(reqID, key, fmt.Errorf("%w: short write: wrote %d of %d bytes", quorumerr.ErrStorageIO, ev.Result, slot.Value.length))
			}
			return true
		}
		s.idx.Set(key, indexEntry{SegID: slot.Value.segID, Offset: slot.Value.offset, Length: slot.Value.length})
		if s.OnWriteComplete != nil {
			s.OnWriteComplete(reqID, key, nil)
		}
		return true
	}

	return false
}

// Stats returns point-in-time statistics across the segment ring.
func (s *Store) Stats() Stats {
	return Stats{
		KeyCount:        int64(s.idx.Len()),
		ActiveSeg:       s.activeSeg,
		TailSeg:         s.tailSeg,
		CurrentEpoch:    s.currentEpoch,
		WALBytesWritten: uint64(s.writeOffset),
	}
}

// Close releases all underlying segment file descriptors.
func (s *Store) Close() error {
	var firstErr error
	for i, f := range s.files {
		if f == nil {
			continue
		}
		err := f.Close()
		s.files[i] = nil
		if err != nil && firstErr == nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EBADF) {
			firstErr = fmt.Errorf("%w: closing segment file %d: %v", quorumerr.ErrStorageIO, i, err)
		}
	}
	return firstErr
}
