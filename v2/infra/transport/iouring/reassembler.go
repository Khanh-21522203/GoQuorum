package iouring

import (
	"goquorum.io/v2/infra/pool"
)

// DefaultReassemblerCap is the initial capacity allocated for TCP stream reassembly.
const DefaultReassemblerCap = 64 * 1024

// Reassembler reassembles streaming TCP byte chunks into discrete framed messages.
// Designed with a sliding buffer backed by pool.BucketArrayPool[byte] for 0 allocations.
type Reassembler struct {
	pool  *pool.BucketArrayPool[byte]
	buf   []byte
	read  int
	write int
}

// Init initializes an inline Reassembler with a buffer rented from bp.
func (r *Reassembler) Init(bp *pool.BucketArrayPool[byte], initialCap int) {
	if bp == nil {
		bp = pool.NewDefaultArrayPool[byte]()
	}
	if initialCap < DefaultReassemblerCap {
		initialCap = DefaultReassemblerCap
	}
	buf := bp.Rent(initialCap)
	if cap(buf) > 0 {
		buf = buf[:cap(buf)]
	}
	r.pool = bp
	r.buf = buf
	r.read = 0
	r.write = 0
}

// NewReassembler creates a Reassembler with an initial buffer rented from the pool.
func NewReassembler(bp *pool.BucketArrayPool[byte], initialCap int) *Reassembler {
	r := &Reassembler{}
	r.Init(bp, initialCap)
	return r
}

// Feed appends newly received socket bytes into the sliding reassembly buffer.
func (r *Reassembler) Feed(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if cap(r.buf) == 0 {
		if r.pool == nil {
			r.pool = pool.NewDefaultArrayPool[byte]()
		}
		r.buf = r.pool.Rent(DefaultReassemblerCap)
		r.buf = r.buf[:cap(r.buf)]
	}

	avail := cap(r.buf) - r.write
	if avail < len(chunk) {
		unread := r.write - r.read
		if unread > 0 && r.read > 0 {
			copy(r.buf[:unread], r.buf[r.read:r.write])
		}
		r.read = 0
		r.write = unread

		needed := r.write + len(chunk)
		if needed > cap(r.buf) {
			newCap := cap(r.buf) * 2
			for newCap < needed {
				newCap *= 2
			}
			newBuf := r.pool.Rent(newCap)
			newBuf = newBuf[:cap(newBuf)]
			if r.write > 0 {
				copy(newBuf[:r.write], r.buf[:r.write])
			}
			r.pool.Return(r.buf)
			r.buf = newBuf
		}
	}

	copy(r.buf[r.write:r.write+len(chunk)], chunk)
	r.write += len(chunk)
}

// Next extracts the next complete framed message if available, returning a zero-copy subslice view.
func (r *Reassembler) Next() (FrameHeader, []byte, bool) {
	if r.write-r.read < FrameHeaderSize {
		return FrameHeader{}, nil, false
	}

	hdr, body, consumed, err := DecodeFrame(r.buf[r.read:r.write])
	if err != nil {
		return FrameHeader{}, nil, false
	}

	r.read += consumed
	if r.read == r.write {
		r.read = 0
		r.write = 0
	}
	return hdr, body, true
}

// Release returns the reassembly buffer to the pool.
func (r *Reassembler) Release() {
	if r.pool != nil && cap(r.buf) > 0 {
		r.pool.Return(r.buf)
		r.buf = nil
	}
	r.read = 0
	r.write = 0
}
