package reactor

import (
	"container/heap"
	"time"
)

// taskQueueCapacity bounds how many PostFunc calls may be outstanding
// before the caller's goroutine blocks. Sized generously since a Reactor
// drains the queue once per loop iteration between I/O polls.
const taskQueueCapacity = 4096

// idleCap bounds how long Poll may block when no timer is scheduled, so
// RequestStop is noticed promptly even if an EventSource's Wake has
// unexpected latency.
const idleCap = 1 * time.Second

// Reactor is a single-threaded event loop. See the package doc for the
// concurrency guarantee it provides.
type Reactor struct {
	source  EventSource
	handler func(Event)

	tasks chan func()
	stop  chan struct{}
	done  chan struct{}

	timers      timerHeap
	byID        map[TimerID]*timer
	nextTimerID TimerID
}

// New constructs a Reactor driven by source. Call SetEventHandler before
// Run; Run itself may be called at most once.
func New(source EventSource) *Reactor {
	return &Reactor{
		source: source,
		tasks:  make(chan func(), taskQueueCapacity),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		byID:   make(map[TimerID]*timer),
	}
}

// SetEventHandler registers the callback invoked for every Event the
// EventSource produces. It must be called before Run and never again.
func (r *Reactor) SetEventHandler(fn func(Event)) {
	r.handler = fn
}

// ScheduleOnce runs fn once, after ~after has elapsed. Safe to call only
// from the reactor's own goroutine (i.e. from within Run, a timer
// callback, an event handler, or a task posted via PostFunc).
func (r *Reactor) ScheduleOnce(after time.Duration, fn func()) TimerID {
	return r.schedule(after, 0, fn)
}

// ScheduleEvery runs fn repeatedly, waiting ~interval between the end of
// one call and the start of the next: fixed-delay, not fixed-rate, so a
// slow fn can never cause a burst of queued-up catch-up calls. Safe to
// call only from the reactor's own goroutine.
func (r *Reactor) ScheduleEvery(interval time.Duration, fn func()) TimerID {
	return r.schedule(interval, interval, fn)
}

func (r *Reactor) schedule(after, interval time.Duration, fn func()) TimerID {
	r.nextTimerID++
	id := r.nextTimerID
	t := &timer{id: id, fireAt: time.Now().Add(after), interval: interval, fn: fn}
	heap.Push(&r.timers, t)
	r.byID[id] = t
	return id
}

// CancelTimer stops a scheduled timer, including one canceling itself from
// within its own callback. Canceling an already-fired one-shot or an
// unknown ID is a no-op. Safe to call only from the reactor's own
// goroutine.
func (r *Reactor) CancelTimer(id TimerID) {
	t, ok := r.byID[id]
	if !ok {
		return
	}
	t.canceled = true
	if t.index >= 0 {
		heap.Remove(&r.timers, t.index)
		delete(r.byID, id)
	}
	// Else: t is currently executing (popped, index == -1); fireDueTimers
	// observes t.canceled once fn returns and drops it from byID itself.
}

// PostFunc schedules fn to run on the reactor's goroutine as soon as
// possible. It is the only Reactor method safe to call from a goroutine
// other than the one running Run — the bridge for callers outside the
// reactor (e.g. an HTTP handler goroutine) to touch reactor-owned state
// safely instead of locking it.
func (r *Reactor) PostFunc(fn func()) {
	r.tasks <- fn
	_ = r.source.Wake()
}

// RequestStop asks Run to return once the current loop iteration's pending
// tasks and due timers have been drained. Safe to call from any goroutine,
// including the reactor's own.
func (r *Reactor) RequestStop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	_ = r.source.Wake()
}

// Run blocks the calling goroutine, which becomes the reactor's single
// thread of execution, until RequestStop is called or the EventSource
// returns an error. Run must be called at most once.
func (r *Reactor) Run() error {
	defer close(r.done)
	events := make([]Event, 0, 64)
	for {
		r.drainTasks()
		r.fireDueTimers()

		select {
		case <-r.stop:
			return nil
		default:
		}

		events = events[:0]
		polled, err := r.source.Poll(events, r.nextDeadline())
		if err != nil {
			return err
		}
		for _, ev := range polled {
			if r.handler != nil {
				r.handler(ev)
			}
		}
	}
}

func (r *Reactor) drainTasks() {
	for {
		select {
		case fn := <-r.tasks:
			fn()
		default:
			return
		}
	}
}

func (r *Reactor) fireDueTimers() {
	now := time.Now()
	for r.timers.Len() > 0 && !r.timers[0].fireAt.After(now) {
		t := heap.Pop(&r.timers).(*timer)
		t.fn()
		if t.canceled {
			delete(r.byID, t.id)
			continue
		}
		if t.interval > 0 {
			t.fireAt = time.Now().Add(t.interval)
			heap.Push(&r.timers, t)
			continue
		}
		delete(r.byID, t.id)
	}
}

func (r *Reactor) nextDeadline() time.Time {
	cap := time.Now().Add(idleCap)
	if r.timers.Len() == 0 {
		return cap
	}
	if next := r.timers[0].fireAt; next.Before(cap) {
		return next
	}
	return cap
}
