package pool

import (
	"math/bits"
)

// ArrayPool is a generic pool for reusable slices of type T,
// inspired by .NET's ArrayPool<T>.
type ArrayPool[T any] interface {
	// Rent returns a slice of type T with capacity >= minCap and length 0.
	Rent(minCap int) []T
	// Return returns a previously rented slice to the pool.
	Return(buf []T)
}

type bucket[T any] struct {
	capacity int
	stack    [][]T
}

// BucketArrayPool is a power-of-two bucketed, lock-free array pool designed for
// single-threaded or thread-confined high-throughput execution.
type BucketArrayPool[T any] struct {
	minCap       int
	maxPerBucket int
	buckets      []bucket[T]
}

// NewArrayPool creates an ArrayPool with customizable bucketing parameters.
func NewArrayPool[T any](minCap, numBuckets, maxPerBucket int) *BucketArrayPool[T] {
	if minCap < 16 {
		minCap = 16
	}
	// Round minCap up to next power of two
	minCap = 1 << bits.Len(uint(minCap-1))

	if numBuckets <= 0 {
		numBuckets = 12 // Up to 64K elements with minCap=16
	}
	if maxPerBucket <= 0 {
		maxPerBucket = 8
	}

	buckets := make([]bucket[T], numBuckets)
	capVal := minCap
	for i := 0; i < numBuckets; i++ {
		buckets[i] = bucket[T]{
			capacity: capVal,
			stack:    make([][]T, 0, maxPerBucket),
		}
		capVal <<= 1
	}

	return &BucketArrayPool[T]{
		minCap:       minCap,
		maxPerBucket: maxPerBucket,
		buckets:      buckets,
	}
}

// NewDefaultArrayPool creates a standard ArrayPool suitable for general slice pooling.
func NewDefaultArrayPool[T any]() *BucketArrayPool[T] {
	return NewArrayPool[T](16, 12, 8)
}

// Rent returns a slice with length 0 and capacity >= minCap.
func (p *BucketArrayPool[T]) Rent(minCap int) []T {
	if minCap <= 0 {
		minCap = 1
	}

	idx := p.bucketIndex(minCap)
	if idx >= len(p.buckets) {
		// Requested capacity exceeds largest bucket: allocate directly
		return make([]T, 0, minCap)
	}

	b := &p.buckets[idx]
	if n := len(b.stack); n > 0 {
		buf := b.stack[n-1]
		b.stack = b.stack[:n-1]
		return buf[:0]
	}

	return make([]T, 0, b.capacity)
}

// Return returns a slice to the matching bucket if capacity allows.
func (p *BucketArrayPool[T]) Return(buf []T) {
	c := cap(buf)
	if c < p.minCap {
		return
	}

	idx := p.bucketIndex(c)
	if idx >= len(p.buckets) {
		return
	}

	b := &p.buckets[idx]
	if len(b.stack) < p.maxPerBucket {
		b.stack = append(b.stack, buf[:0])
	}
}

func (p *BucketArrayPool[T]) bucketIndex(capacity int) int {
	if capacity <= p.minCap {
		return 0
	}
	// Calculate power-of-two offset from minCap
	diff := (capacity - 1) / p.minCap
	return bits.Len(uint(diff))
}
