package reactor

import "time"

// TimerID identifies a scheduled timer for cancellation.
type TimerID uint64

type timer struct {
	id       TimerID
	fireAt   time.Time
	interval time.Duration // 0 = one-shot
	fn       func()
	index    int // heap index; -1 while the timer is executing
	canceled bool
}

// timerHeap is a container/heap min-heap ordered by fireAt.
type timerHeap []*timer

func (h timerHeap) Len() int { return len(h) }

func (h timerHeap) Less(i, j int) bool { return h[i].fireAt.Before(h[j].fireAt) }

func (h timerHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *timerHeap) Push(x any) {
	t := x.(*timer)
	t.index = len(*h)
	*h = append(*h, t)
}

func (h *timerHeap) Pop() any {
	old := *h
	n := len(old)
	t := old[n-1]
	old[n-1] = nil
	t.index = -1
	*h = old[:n-1]
	return t
}
