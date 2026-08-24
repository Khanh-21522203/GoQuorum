package reactor

import "time"

// Event is a single completed operation delivered by an EventSource.
// UserData is caller-defined and is typically used to correlate an Event
// back to whatever submitted the operation it completes.
type Event struct {
	UserData uint64
	Result   int64
	Err      error
}

// EventSource is the [PORT] an infra adapter implements to drive a
// Reactor's I/O. A Reactor's single-thread guarantee only holds if Poll
// performs its own wait on the calling goroutine rather than delegating to
// another one, so implementations must document any deviation from that.
type EventSource interface {
	// Poll appends every currently-ready Event to dst and returns the
	// extended slice, blocking until at least one is ready or deadline
	// passes. A zero deadline means return immediately with whatever is
	// already ready, without blocking.
	Poll(dst []Event, deadline time.Time) ([]Event, error)
	// Wake unblocks a goroutine currently parked in Poll, from any other
	// goroutine. It must be safe to call concurrently with Poll and with
	// itself.
	Wake() error
	// Close releases all resources held by the event source.
	Close() error
}
