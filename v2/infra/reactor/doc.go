// Package reactor implements a single-threaded run loop: exactly one
// goroutine, the one that calls (*Reactor).Run, ever executes timer
// callbacks, posted tasks, or event handlers. Every engine subsystem built
// on top of a Reactor may hold plain unsynchronized state, since nothing
// else ever touches it concurrently.
package reactor
