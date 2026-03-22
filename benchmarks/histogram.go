package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Histogram collects latency samples and computes percentiles.
// All samples are stored in nanoseconds.
type Histogram struct {
	mu      sync.Mutex
	samples []int64
}

func (h *Histogram) Record(d time.Duration) {
	h.mu.Lock()
	h.samples = append(h.samples, d.Nanoseconds())
	h.mu.Unlock()
}

func (h *Histogram) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.samples)
}

// Sorted returns a sorted copy of all samples.
func (h *Histogram) Sorted() []int64 {
	h.mu.Lock()
	cp := make([]int64, len(h.samples))
	copy(cp, h.samples)
	h.mu.Unlock()
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp
}

// Percentile returns the p-th percentile of sorted (must be pre-sorted via Sorted()).
func Percentile(sorted []int64, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p / 100.0)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return time.Duration(sorted[idx])
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%d ns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.1f µs", float64(d.Nanoseconds())/1e3)
	default:
		return fmt.Sprintf("%.2f ms", float64(d.Nanoseconds())/1e6)
	}
}

// Result holds the outcome of a single workload run.
type Result struct {
	Name     string
	Duration time.Duration
	Hist     *Histogram
	Errors   int64
}

func (r *Result) Print() {
	sorted := r.Hist.Sorted()
	count := len(sorted)

	fmt.Printf("┌─ %s\n", r.Name)
	if count == 0 {
		fmt.Printf("│  no samples (errors: %d)\n└\n\n", r.Errors)
		return
	}

	qps := float64(count) / r.Duration.Seconds()
	p50 := Percentile(sorted, 50)
	p90 := Percentile(sorted, 90)
	p99 := Percentile(sorted, 99)
	p999 := Percentile(sorted, 99.9)
	min := Percentile(sorted, 0)
	max := Percentile(sorted, 100)

	fmt.Printf("│  ops:     %d  over %s\n", count, r.Duration.Round(time.Millisecond))
	fmt.Printf("│  QPS:     %.0f ops/sec\n", qps)
	fmt.Printf("│  min:     %s\n", fmtDuration(min))
	fmt.Printf("│  P50:     %s\n", fmtDuration(p50))
	fmt.Printf("│  P90:     %s\n", fmtDuration(p90))
	fmt.Printf("│  P99:     %s\n", fmtDuration(p99))
	fmt.Printf("│  P99.9:   %s\n", fmtDuration(p999))
	fmt.Printf("│  max:     %s\n", fmtDuration(max))
	fmt.Printf("└  errors:  %d\n\n", r.Errors)
}
