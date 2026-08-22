package pool

const (
	// DefaultArenaChunkSize is the default initial byte capacity for arena chunks (64 KB).
	DefaultArenaChunkSize = 64 * 1024
)

// ByteArena is a fast, chunk-chained linear bump allocator backed by a BucketArrayPool[byte].
// When a chunk fills up, it chains a new chunk from the pool without reallocating or moving old chunks,
// guaranteeing that previously returned subslices remain permanently stable and valid.
type ByteArena struct {
	pool      *BucketArrayPool[byte]
	chunkSize int
	chunks    [][]byte
	cur       int
}

// NewByteArena creates a ByteArena with the specified pool and default chunk capacity.
func NewByteArena(p *BucketArrayPool[byte], chunkSize int) *ByteArena {
	if chunkSize <= 0 {
		chunkSize = DefaultArenaChunkSize
	}
	firstChunk := p.Rent(chunkSize)
	return &ByteArena{
		pool:      p,
		chunkSize: chunkSize,
		chunks:    [][]byte{firstChunk[:0]},
		cur:       0,
	}
}

// Alloc copies src into the arena and returns a stable, zero-alloc subslice pointing to the arena's memory.
func (a *ByteArena) Alloc(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}

	curChunk := a.chunks[a.cur]
	// If current chunk has enough spare capacity:
	if len(curChunk)+len(src) <= cap(curChunk) {
		start := len(curChunk)
		a.chunks[a.cur] = append(curChunk, src...)
		return a.chunks[a.cur][start : start+len(src)]
	}

	// Current chunk is full: allocate a new chained chunk from the pool
	nextCap := a.chunkSize
	if len(src) > nextCap {
		nextCap = len(src)
	}
	newChunk := a.pool.Rent(nextCap)
	newChunk = append(newChunk[:0], src...)
	a.chunks = append(a.chunks, newChunk)
	a.cur++

	return newChunk[:len(src)]
}

// AllocString copies string s into the arena and returns a stable byte subslice.
func (a *ByteArena) AllocString(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	curChunk := a.chunks[a.cur]
	if len(curChunk)+len(s) <= cap(curChunk) {
		start := len(curChunk)
		a.chunks[a.cur] = append(a.chunks[a.cur], s...)
		return a.chunks[a.cur][start : start+len(s)]
	}

	nextCap := a.chunkSize
	if len(s) > nextCap {
		nextCap = len(s)
	}
	newChunk := a.pool.Rent(nextCap)
	newChunk = append(newChunk[:0], s...)
	a.chunks = append(a.chunks, newChunk)
	a.cur++

	return newChunk[:len(s)]
}

// ChunkCount returns the number of active chunks in the arena chain.
func (a *ByteArena) ChunkCount() int {
	return len(a.chunks)
}

// Reset clears the arena's chunks for reuse without returning them to the pool.
func (a *ByteArena) Reset() {
	if len(a.chunks) == 0 {
		return
	}
	for i := range a.chunks {
		a.chunks[i] = a.chunks[i][:0]
	}
	a.cur = 0
}

// Release returns all chained chunks back to the BucketArrayPool.
func (a *ByteArena) Release() {
	for _, chunk := range a.chunks {
		a.pool.Return(chunk)
	}
	a.chunks = nil
	a.cur = 0
}
