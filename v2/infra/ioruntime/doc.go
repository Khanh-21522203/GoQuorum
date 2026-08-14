// Package ioruntime bridges github.com/iceber/iouring-go into an
// engine/reactor.EventSource, so io_uring-backed storage and transport
// adapters can drive engine's single-threaded reactor.
//
// # A documented tradeoff
//
// github.com/iceber/iouring-go manages its own internal goroutines: New
// starts a per-ring completion reaper (iour.run) and a shared epoll poller
// goroutine that both block on real syscalls waiting for completions. Poll
// on the Runtime built here therefore does not perform the io_uring wait
// syscall itself — it receives, with a deadline, from a channel those
// internal goroutines deliver completions onto. The property that actually
// matters for correctness still holds exactly: those internal goroutines
// never touch any engine-owned state, they only ever shuttle opaque
// completion values into one channel a Runtime owns, so exactly one
// goroutine — whichever calls (*engine/reactor.Reactor).Run — still ever
// executes engine logic.
package ioruntime
