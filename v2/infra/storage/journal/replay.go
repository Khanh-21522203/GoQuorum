package journal

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"sort"
)

// ReplaySingleSegment replays a single WAL segment file, parsing its 16-byte header
// (if present) and indexing all valid records under segID.
// Stops without error at EOF or the first corrupt/truncated record.
func ReplaySingleSegment(f *os.File, segID uint16, idx *index) (tailOffset int64, epoch uint64, status SegmentStatus, err error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, StatusEmpty, err
	}

	headerBuf := make([]byte, SegmentHeaderSize)
	n, readErr := io.ReadFull(f, headerBuf)

	var offset int64
	if n == SegmentHeaderSize && readErr == nil {
		if ep, st, ok := DecodeSegmentHeader(headerBuf); ok {
			epoch = ep
			status = st
			offset = SegmentHeaderSize
		} else {
			// Unheadered legacy file: rewind to byte 0
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return 0, 0, StatusEmpty, err
			}
			offset = 0
		}
	} else {
		// File smaller than header: rewind to byte 0
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, 0, StatusEmpty, err
		}
		offset = 0
	}

	r := bufio.NewReader(f)
	lengthBuf := make([]byte, recordLengthFieldSize)

	for {
		n, readErr := io.ReadFull(r, lengthBuf)
		if n == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}

		length := binary.BigEndian.Uint32(lengthBuf)
		body := make([]byte, length)
		if _, readErr := io.ReadFull(r, body); readErr != nil {
			break
		}

		full := make([]byte, 0, recordLengthFieldSize+len(body))
		full = append(full, lengthBuf...)
		full = append(full, body...)

		keyView, _, consumed, decErr := DecodeRecord(full)
		if decErr != nil {
			break
		}

		if idx != nil {
			key := append([]byte(nil), keyView...)
			idx.Set(key, indexEntry{
				SegID:  segID,
				Offset: offset,
				Length: uint32(consumed),
			})
		}
		offset += int64(consumed)
	}

	return offset, epoch, status, nil
}

// Replay replays a single file under segID 0 (backwards compatible helper).
func Replay(f *os.File) (*index, int64, error) {
	idx := newIndex()
	tail, _, _, err := ReplaySingleSegment(f, 0, idx)
	return idx, tail, err
}

type segmentMeta struct {
	segID      int
	epoch      uint64
	status     SegmentStatus
	tailOffset int64
}

// ReplayRingSegments uses Status-Guided Log-Structured Replay:
// 1. Pass 1 (O(1) Headers): Scans headers to find the latest StatusCompacted (Base) and latest StatusWriter (Head).
// 2. Pass 2 (Chronological Replay): Replays Base checkpoint first, then subsequent StatusWriter segments in epoch order.
func ReplayRingSegments(files []*os.File) (idx *index, activeSeg int, tailSeg int, maxEpoch uint64, tailOffset int64, err error) {
	idx = newIndex()
	if len(files) == 0 {
		return idx, 0, 0, 0, 0, nil
	}

	metas := make([]segmentMeta, len(files))

	var latestCompactedEpoch uint64
	var latestCompactedSeg = -1

	var latestWriterEpoch uint64
	var latestWriterSeg = -1

	var foundEpoch bool

	// Pass 1: Read all 16-byte headers without reading full bodies
	for segID, f := range files {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, 0, 0, 0, 0, err
		}
		hdr := make([]byte, SegmentHeaderSize)
		n, _ := io.ReadFull(f, hdr)

		var ep uint64
		var st SegmentStatus
		if n == SegmentHeaderSize {
			if e, s, ok := DecodeSegmentHeader(hdr); ok {
				ep = e
				st = s
				foundEpoch = true
			}
		}

		metas[segID] = segmentMeta{
			segID:  segID,
			epoch:  ep,
			status: st,
		}

		if st == StatusCompacted && ep >= latestCompactedEpoch {
			latestCompactedEpoch = ep
			latestCompactedSeg = segID
		}
		if st == StatusWriter && ep >= latestWriterEpoch {
			latestWriterEpoch = ep
			latestWriterSeg = segID
		}
		if ep > maxEpoch {
			maxEpoch = ep
		}
	}

	if !foundEpoch {
		// Fresh uninitialized ring or legacy unheadered files: replay all in array order
		for segID, f := range files {
			tail, _, _, err := ReplaySingleSegment(f, uint16(segID), idx)
			if err != nil {
				return nil, 0, 0, 0, 0, err
			}
			metas[segID].tailOffset = tail
		}
		activeSeg = 0
		tailOffset = metas[0].tailOffset
		for segID := len(files) - 1; segID >= 0; segID-- {
			if metas[segID].tailOffset > 0 {
				activeSeg = segID
				tailOffset = metas[segID].tailOffset
				break
			}
		}
		return idx, activeSeg, 0, 0, tailOffset, nil
	}

	// Pass 2: Determine which segments to replay
	var segmentsToReplay []segmentMeta

	if latestCompactedSeg != -1 {
		// Anchor to latest StatusCompacted checkpoint
		segmentsToReplay = append(segmentsToReplay, metas[latestCompactedSeg])
		tailSeg = latestCompactedSeg

		// Add all StatusWriter segments with epoch > latestCompactedEpoch
		for _, m := range metas {
			if m.status == StatusWriter && m.epoch > latestCompactedEpoch {
				segmentsToReplay = append(segmentsToReplay, m)
			}
		}
	} else {
		// No compaction checkpoint yet: replay all valid StatusWriter segments in epoch window
		minEpoch := uint64(0)
		if maxEpoch > uint64(len(files)-1) {
			minEpoch = maxEpoch - uint64(len(files)-1)
		}
		var minWriterEpoch uint64 = ^uint64(0)
		for _, m := range metas {
			if m.status == StatusWriter && m.epoch >= minEpoch {
				segmentsToReplay = append(segmentsToReplay, m)
				if m.epoch < minWriterEpoch {
					minWriterEpoch = m.epoch
					tailSeg = m.segID
				}
			}
		}
	}

	// Sort segments to replay in ascending chronological Epoch order
	sort.Slice(segmentsToReplay, func(i, j int) bool {
		return segmentsToReplay[i].epoch < segmentsToReplay[j].epoch
	})

	// Replay selected segments chronologically
	for _, m := range segmentsToReplay {
		tail, _, _, err := ReplaySingleSegment(files[m.segID], uint16(m.segID), idx)
		if err != nil {
			return nil, 0, 0, 0, 0, err
		}
		if m.segID == latestWriterSeg {
			tailOffset = tail
		}
	}

	if latestWriterSeg != -1 {
		activeSeg = latestWriterSeg
		if tailOffset == 0 {
			tailOffset = SegmentHeaderSize
		}
	} else if latestCompactedSeg != -1 {
		activeSeg = (latestCompactedSeg + 1) % len(files)
		tailOffset = SegmentHeaderSize
	} else {
		activeSeg = 0
		tailOffset = SegmentHeaderSize
	}

	return idx, activeSeg, tailSeg, maxEpoch, tailOffset, nil
}
