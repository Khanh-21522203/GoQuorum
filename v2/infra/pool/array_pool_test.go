package pool

import (
	"testing"
)

func TestBucketArrayPool_RentAndReturn(t *testing.T) {
	pool := NewDefaultArrayPool[int]()

	// Rent slice with minCap 10 -> Should get minCap=16 bucket
	s1 := pool.Rent(10)
	if cap(s1) < 16 {
		t.Fatalf("expected cap >= 16, got %d", cap(s1))
	}
	if len(s1) != 0 {
		t.Fatalf("expected len == 0, got %d", len(s1))
	}

	// Append some elements
	s1 = append(s1, 1, 2, 3)

	// Return to pool
	pool.Return(s1)

	// Rent again -> Should reuse the same underlying backing array
	s2 := pool.Rent(10)
	if cap(s2) != cap(s1) {
		t.Fatalf("expected reused slice cap %d, got %d", cap(s1), cap(s2))
	}
	if len(s2) != 0 {
		t.Fatalf("expected len reset to 0, got %d", len(s2))
	}
}

func TestBucketArrayPool_PowerOfTwoBucketing(t *testing.T) {
	pool := NewArrayPool[byte](16, 8, 4)

	testCases := []struct {
		reqCap  int
		wantCap int
	}{
		{reqCap: 1, wantCap: 16},
		{reqCap: 16, wantCap: 16},
		{reqCap: 17, wantCap: 32},
		{reqCap: 32, wantCap: 32},
		{reqCap: 33, wantCap: 64},
		{reqCap: 100, wantCap: 128},
		{reqCap: 1024, wantCap: 1024},
		{reqCap: 1025, wantCap: 2048},
	}

	for _, tc := range testCases {
		buf := pool.Rent(tc.reqCap)
		if cap(buf) < tc.wantCap {
			t.Errorf("Rent(%d) cap = %d, want >= %d", tc.reqCap, cap(buf), tc.wantCap)
		}
		pool.Return(buf)
	}
}

func TestBucketArrayPool_ExceedsMaxBucketCapacity(t *testing.T) {
	pool := NewArrayPool[byte](16, 2, 2) // Max cap = 16 << 1 = 32

	// Request 1000 bytes (larger than largest bucket)
	buf := pool.Rent(1000)
	if cap(buf) < 1000 {
		t.Fatalf("expected cap >= 1000, got %d", cap(buf))
	}

	// Return oversized buffer (should not panic or corrupt buckets)
	pool.Return(buf)
}
