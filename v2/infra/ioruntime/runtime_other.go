//go:build !linux

package ioruntime

import (
	"time"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/infra/reactor"
)

// Runtime is unavailable on non-Linux platforms: io_uring is a Linux
// kernel facility with no portable equivalent.
type Runtime struct{}

var _ reactor.EventSource = (*Runtime)(nil)

// New always fails on non-Linux platforms.
func New(queueDepth uint) (*Runtime, error) {
	return nil, contracts.ErrNotImplemented
}

// Poll implements engine/reactor.EventSource.
func (r *Runtime) Poll(dst []reactor.Event, deadline time.Time) ([]reactor.Event, error) {
	return dst, contracts.ErrNotImplemented
}

// Wake implements engine/reactor.EventSource.
func (r *Runtime) Wake() error {
	return contracts.ErrNotImplemented
}

// Close implements engine/reactor.EventSource.
func (r *Runtime) Close() error {
	return contracts.ErrNotImplemented
}

// SubmitRead is unavailable on non-Linux platforms.
func (r *Runtime) SubmitRead(fd int, buf []byte, userData uint64) error {
	return contracts.ErrNotImplemented
}

// SubmitWrite is unavailable on non-Linux platforms.
func (r *Runtime) SubmitWrite(fd int, buf []byte, userData uint64) error {
	return contracts.ErrNotImplemented
}

// SubmitPread is unavailable on non-Linux platforms.
func (r *Runtime) SubmitPread(fd int, buf []byte, offset uint64, userData uint64) error {
	return contracts.ErrNotImplemented
}

// SubmitPwrite is unavailable on non-Linux platforms.
func (r *Runtime) SubmitPwrite(fd int, buf []byte, offset uint64, userData uint64) error {
	return contracts.ErrNotImplemented
}

// SubmitAccept is unavailable on non-Linux platforms.
func (r *Runtime) SubmitAccept(fd int, userData uint64) error {
	return contracts.ErrNotImplemented
}

// SubmitRecv is unavailable on non-Linux platforms.
func (r *Runtime) SubmitRecv(fd int, buf []byte, userData uint64) error {
	return contracts.ErrNotImplemented
}

// SubmitSend is unavailable on non-Linux platforms.
func (r *Runtime) SubmitSend(fd int, buf []byte, userData uint64) error {
	return contracts.ErrNotImplemented
}

// SubmitClose is unavailable on non-Linux platforms.
func (r *Runtime) SubmitClose(fd int, userData uint64) error {
	return contracts.ErrNotImplemented
}
