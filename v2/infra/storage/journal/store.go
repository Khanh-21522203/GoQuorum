package journal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
)

const (
	// MaxCoalesceGap is the maximum byte gap between two adjacent records to merge them into a single sequential Pread.
	MaxCoalesceGap = 64 * 1024 // 64 KB
	// MaxCoalesceChunkSize is the maximum total byte span of a single coalesced Pread.
	MaxCoalesceChunkSize = 2 * 1024 * 1024 // 2 MB

	// scanUserDataFlag is set on bit 62 of io_uring UserData to distinguish scan chunk reads from standard Get reads.
	scanUserDataFlag = uint64(1) << 62
)

// Options configures Store initialization.
type Options struct {
	Path string // Path to the on-disk WAL file
}

// Stats reports point-in-time storage statistics.
type Stats struct {
	KeyCount        int64
	WALBytesWritten uint64
}

// ScanEntry represents a raw key-value pair returned in a scan batch.
type ScanEntry struct {
	Key   []byte
	Value []byte
}

type inFlightWrite struct {
	key    []byte
	offset int64
	length uint32
}

// scanItem represents a single matched record in the flat scan item array.
type scanItem struct {
	keyIndex int    // Destination slot in final sorted results []ScanEntry
	offset   int64  // Physical file offset in WAL
	length   uint32 // Record length in bytes
}

// scanChunk represents a contiguous physical disk read covering a range of scanItems [startItem, endItem).
type scanChunk struct {
	offset    int64  // Starting byte offset in WAL
	length    uint32 // Total byte length to read
	startItem int    // First item index in scanState.items
	endItem   int    // Exclusive end item index in scanState.items
	buf       []byte // Rented disk buffer
}

type inFlightScan struct {
	items        []scanItem
	chunks       []scanChunk
	results      []ScanEntry
	pendingCount int
	failedErr    error
}

// Store is an event-driven, append-only Key-Value WAL backed by io_uring.
type Store struct {
	f    *os.File
	fd   int
	rt   *ioruntime.Runtime
	opts Options

	idx *index

	writeOffset int64 // Next WAL byte offset (allocated at submit time)

	inFlightReads  map[uint64][]byte        // reqID -> buffer (for Get)
	inFlightWrites map[uint64]inFlightWrite // reqID -> write metadata (for Put)
	inFlightScans  map[uint64]*inFlightScan // scanID -> scan state

	scanPool pool.ArrayPool[ScanEntry] // ArrayPool managing reusable slice buffers

	// Event Hooks (Registered by higher layer)
	OnReadComplete  func(reqID uint64, key []byte, val []byte, err error)
	OnWriteComplete func(reqID uint64, key []byte, err error)
	OnScanComplete  func(scanID uint64, items []ScanEntry, err error)
	OnStorageError  func(err error)
}

// Open opens (or creates) the WAL file, synchronously replays existing records
// to rebuild the in-memory index, and returns an initialized Store.
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
		f:              f,
		fd:             int(f.Fd()),
		rt:             rt,
		opts:           opts,
		idx:            idx,
		writeOffset:    tail,
		inFlightReads:  make(map[uint64][]byte),
		inFlightWrites: make(map[uint64]inFlightWrite),
		inFlightScans:  make(map[uint64]*inFlightScan),
		scanPool:       pool.NewDefaultArrayPool[ScanEntry](),
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

// SetOnStorageError registers the storage error event hook.
func (s *Store) SetOnStorageError(fn func(err error)) {
	s.OnStorageError = fn
}

// Get issues an asynchronous read request for key tagged with reqID.
// Emits OnReadComplete upon completion.
func (s *Store) Get(reqID uint64, key []byte) error {
	entry, ok := s.idx.Get(key)
	if !ok {
		if s.OnReadComplete != nil {
			s.OnReadComplete(reqID, key, nil, quorumerr.ErrKeyNotFound)
		}
		return nil
	}

	buf := make([]byte, entry.Length)
	s.inFlightReads[reqID] = buf

	if err := s.rt.SubmitPread(s.fd, buf, uint64(entry.Offset), reqID); err != nil {
		delete(s.inFlightReads, reqID)
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
// Emits OnWriteComplete upon completion.
func (s *Store) Put(reqID uint64, key []byte, val []byte) error {
	buf, err := EncodeRecord(key, val)
	if err != nil {
		return fmt.Errorf("%w: encoding record: %v", quorumerr.ErrStorageIO, err)
	}

	offset := s.writeOffset
	s.writeOffset += int64(len(buf))

	s.inFlightWrites[reqID] = inFlightWrite{
		key:    append([]byte(nil), key...),
		offset: offset,
		length: uint32(len(buf)),
	}

	if err := s.rt.SubmitPwrite(s.fd, buf, uint64(offset), reqID); err != nil {
		delete(s.inFlightWrites, reqID)
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
// Chunks define index ranges [startItem, endItem) over a single flat items array with zero nested allocations.
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

	// 1. Prepare flat items array tracking original sorted key index
	items := make([]scanItem, 0, len(matched))
	for i, k := range matched {
		entry, ok := s.idx.Get(k)
		if !ok {
			continue
		}
		items = append(items, scanItem{
			keyIndex: i,
			offset:   entry.Offset,
			length:   entry.Length,
		})
	}

	if len(items) == 0 {
		if s.OnScanComplete != nil {
			s.OnScanComplete(scanID, nil, nil)
		}
		return nil
	}

	// 2. Sort items by physical file offset (Forward-Sweep disk locality)
	sort.Slice(items, func(i, j int) bool {
		return items[i].offset < items[j].offset
	})

	// 3. Coalesce items into chunks using index ranges [startItem, endItem)
	chunks := coalesceScanItems(items)

	results := s.scanPool.Rent(len(matched))
	results = results[:len(matched)] // Pre-expand length to allow direct slot assignment

	scanState := &inFlightScan{
		items:        items,
		chunks:       chunks,
		results:      results,
		pendingCount: len(chunks),
	}
	s.inFlightScans[scanID] = scanState

	// 4. Submit all coalesced chunk reads in parallel to io_uring with bit-packed UserData
	for chunkIdx := range scanState.chunks {
		chunk := &scanState.chunks[chunkIdx]
		chunk.buf = make([]byte, chunk.length)

		// Bit-pack: Flag (bit 62) | ScanID (bits 16..61) | ChunkIndex (bits 0..15)
		userData := scanUserDataFlag | ((scanID & 0x3FFFFFFFFFFF) << 16) | uint64(chunkIdx&0xFFFF)

		if err := s.rt.SubmitPread(s.fd, chunk.buf, uint64(chunk.offset), userData); err != nil {
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
		delete(s.inFlightScans, scanID)
		if s.OnScanComplete != nil {
			s.OnScanComplete(scanID, nil, scanState.failedErr)
		}
		s.scanPool.Return(results)
	}

	return nil
}

// coalesceScanItems groups forward-sorted scan items into chunks using index ranges [startItem, endItem).
func coalesceScanItems(items []scanItem) []scanChunk {
	if len(items) == 0 {
		return nil
	}

	chunks := make([]scanChunk, 0, len(items))
	curChunk := scanChunk{
		offset:    items[0].offset,
		length:    items[0].length,
		startItem: 0,
		endItem:   1,
	}

	for i := 1; i < len(items); i++ {
		item := items[i]
		chunkEnd := curChunk.offset + int64(curChunk.length)
		gap := item.offset - chunkEnd
		newTotalLen := curChunk.length + uint32(gap) + item.length

		if gap >= 0 && gap <= MaxCoalesceGap && newTotalLen <= MaxCoalesceChunkSize {
			curChunk.length = newTotalLen
			curChunk.endItem = i + 1
		} else {
			chunks = append(chunks, curChunk)
			curChunk = scanChunk{
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

// HandleCompletion dispatches an io_uring completion event to the awaiting event hook.
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
				// Unpack all records belonging to this chunk's index range [startItem, endItem)
				for i := chunk.startItem; i < chunk.endItem; i++ {
					item := scanState.items[i]
					relOffset := uint32(item.offset - chunk.offset)
					recordBytes := chunk.buf[relOffset : relOffset+item.length]
					key, val, _, err := DecodeRecord(recordBytes)
					if err != nil && scanState.failedErr == nil {
						scanState.failedErr = err
						break
					} else if err == nil {
						scanState.results[item.keyIndex] = ScanEntry{
							Key:   key,
							Value: val,
						}
					}
				}
			}

			scanState.pendingCount--
			if scanState.pendingCount == 0 {
				// All chunks for this scan have finished!
				results := scanState.results
				failedErr := scanState.failedErr
				delete(s.inFlightScans, scanID)

				if s.OnScanComplete != nil {
					if failedErr != nil {
						s.OnScanComplete(scanID, nil, failedErr)
					} else {
						s.OnScanComplete(scanID, results, nil)
					}
				}
				s.scanPool.Return(results)
			}
		}
		return true
	}

	// 2. Check if it's a standard Get read completion
	if buf, ok := s.inFlightReads[reqID]; ok {
		delete(s.inFlightReads, reqID)

		if ev.Err != nil {
			if s.OnReadComplete != nil {
				s.OnReadComplete(reqID, nil, nil, fmt.Errorf("%w: reading record: %v", quorumerr.ErrStorageIO, ev.Err))
			}
			return true
		}
		if int(ev.Result) != len(buf) {
			if s.OnReadComplete != nil {
				s.OnReadComplete(reqID, nil, nil, fmt.Errorf("%w: short read: got %d of %d bytes", quorumerr.ErrStorageIO, ev.Result, len(buf)))
			}
			return true
		}
		key, val, _, err := DecodeRecord(buf)
		if s.OnReadComplete != nil {
			s.OnReadComplete(reqID, key, val, err)
		}
		return true
	}

	// 3. Check if it's a write completion (Put / Delete)
	if w, ok := s.inFlightWrites[reqID]; ok {
		delete(s.inFlightWrites, reqID)
		if ev.Err != nil {
			if s.OnWriteComplete != nil {
				s.OnWriteComplete(reqID, w.key, fmt.Errorf("%w: writing record: %v", quorumerr.ErrStorageIO, ev.Err))
			}
			return true
		}
		if int(ev.Result) != int(w.length) {
			if s.OnWriteComplete != nil {
				s.OnWriteComplete(reqID, w.key, fmt.Errorf("%w: short write: wrote %d of %d bytes", quorumerr.ErrStorageIO, ev.Result, w.length))
			}
			return true
		}
		s.idx.Set(w.key, indexEntry{Offset: w.offset, Length: w.length})
		if s.OnWriteComplete != nil {
			s.OnWriteComplete(reqID, w.key, nil)
		}
		return true
	}

	return false
}

// Stats returns point-in-time statistics for the raw storage engine.
func (s *Store) Stats() Stats {
	return Stats{
		KeyCount:        int64(s.idx.Len()),
		WALBytesWritten: uint64(s.writeOffset),
	}
}

// Close releases the underlying WAL file descriptor.
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
