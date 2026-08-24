//go:build linux

package ioruntime

import (
	"errors"
	"syscall"
	"time"

	iouring "github.com/iceber/iouring-go"
	iouringsyscall "github.com/iceber/iouring-go/syscall"

	"goquorum.io/v2/infra/reactor"
)

// errNotARequest is returned when a delivered iouring.Result does not also
// implement iouring.Request. The library always delivers the same *request
// value it returned from SubmitRequest on the channel, so this indicates a
// library-internal change this wrapper no longer matches.
var errNotARequest = errors.New("ioruntime: completion value does not implement iouring.Request")

// resultQueueCapacity bounds how many completed operations may be
// outstanding before the internal reaper goroutines documented in the
// package doc block on delivering another one.
const resultQueueCapacity = 4096

// Runtime is an engine/reactor.EventSource backed by a real io_uring
// instance. See the package doc for the concurrency tradeoff it makes.
type Runtime struct {
	ring    *iouring.IOURing
	results chan iouring.Result
	wake    chan struct{}
}

var _ reactor.EventSource = (*Runtime)(nil)

// New opens an io_uring instance with the given submission queue depth and
// wraps it as a Runtime.
func New(queueDepth uint) (*Runtime, error) {
	ring, err := iouring.New(queueDepth)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		ring:    ring,
		results: make(chan iouring.Result, resultQueueCapacity),
		wake:    make(chan struct{}, 1),
	}, nil
}

// Poll implements engine/reactor.EventSource.
func (r *Runtime) Poll(dst []reactor.Event, deadline time.Time) ([]reactor.Event, error) {
	dst = r.drainReady(dst)
	if len(dst) > 0 {
		return dst, nil
	}

	if deadline.IsZero() {
		return dst, nil
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return dst, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case res := <-r.results:
		dst = append(dst, toEvent(res))
	case <-r.wake:
	case <-timer.C:
	}
	return dst, nil
}

func (r *Runtime) drainReady(dst []reactor.Event) []reactor.Event {
	for {
		select {
		case res := <-r.results:
			dst = append(dst, toEvent(res))
		default:
			return dst
		}
	}
}

// Wake implements engine/reactor.EventSource.
func (r *Runtime) Wake() error {
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// Close implements engine/reactor.EventSource.
func (r *Runtime) Close() error {
	return r.ring.Close()
}

// toEvent reads a completion's raw result directly off the underlying
// Request rather than through Result.ReturnInt(): this library only
// populates ReturnInt's backing field for operations it attaches a result
// "resolver" to (e.g. Accept), and leaves it unset for others (e.g. Send,
// Recv) — which ReturnInt reports as "value is not int" instead of the
// real outcome. GetRes returns the completion queue entry's raw result for
// every operation uniformly, so the negative-errno decoding below is done
// once, here, instead of depending on per-operation library behavior.
func toEvent(res iouring.Result) reactor.Event {
	userData, _ := res.GetRequestInfo().(uint64)

	req, ok := res.(iouring.Request)
	if !ok {
		return reactor.Event{UserData: userData, Err: errNotARequest}
	}
	n, err := req.GetRes()
	if err != nil {
		return reactor.Event{UserData: userData, Err: err}
	}
	if n < 0 {
		errno := syscall.Errno(-n)
		if errno == syscall.ECANCELED {
			return reactor.Event{UserData: userData, Err: iouring.ErrRequestCanceled}
		}
		return reactor.Event{UserData: userData, Err: errno}
	}
	return reactor.Event{UserData: userData, Result: int64(n)}
}

// withUserData wraps an iouring.PrepRequest so its completion's
// GetRequestInfo() returns userData, letting Poll's caller correlate a
// reactor.Event back to whatever submitted it.
func withUserData(base iouring.PrepRequest, userData uint64) iouring.PrepRequest {
	return func(sqe iouringsyscall.SubmissionQueueEntry, ud *iouring.UserData) {
		ud.SetRequestInfo(userData)
		base(sqe, ud)
	}
}

func (r *Runtime) submit(base iouring.PrepRequest, userData uint64) error {
	_, err := r.ring.SubmitRequest(withUserData(base, userData), r.results)
	return err
}

// SubmitRead submits an async read of len(buf) bytes from fd at the
// current file offset. The completion's Event.Result is the byte count
// read (or negative/zero at EOF), delivered with UserData set to userData.
func (r *Runtime) SubmitRead(fd int, buf []byte, userData uint64) error {
	return r.submit(iouring.Read(fd, buf), userData)
}

// SubmitWrite submits an async write of buf to fd at the current file
// offset.
func (r *Runtime) SubmitWrite(fd int, buf []byte, userData uint64) error {
	return r.submit(iouring.Write(fd, buf), userData)
}

// SubmitPread submits an async positioned read of len(buf) bytes from fd
// at offset, leaving the file's current offset untouched.
func (r *Runtime) SubmitPread(fd int, buf []byte, offset uint64, userData uint64) error {
	return r.submit(iouring.Pread(fd, buf, offset), userData)
}

// SubmitPwrite submits an async positioned write of buf to fd at offset,
// leaving the file's current offset untouched.
func (r *Runtime) SubmitPwrite(fd int, buf []byte, offset uint64, userData uint64) error {
	return r.submit(iouring.Pwrite(fd, buf, offset), userData)
}

// SubmitAccept submits an async accept on the listening socket fd.
// Event.Result on completion is the accepted connection's file descriptor.
func (r *Runtime) SubmitAccept(fd int, userData uint64) error {
	return r.submit(iouring.Accept(fd), userData)
}

// SubmitRecv submits an async recv of up to len(buf) bytes on socket fd.
func (r *Runtime) SubmitRecv(fd int, buf []byte, userData uint64) error {
	return r.submit(iouring.Recv(fd, buf, 0), userData)
}

// SubmitSend submits an async send of buf on socket fd.
func (r *Runtime) SubmitSend(fd int, buf []byte, userData uint64) error {
	return r.submit(iouring.Send(fd, buf, 0), userData)
}

// SubmitClose submits an async close of fd.
func (r *Runtime) SubmitClose(fd int, userData uint64) error {
	return r.submit(iouring.Close(fd), userData)
}
