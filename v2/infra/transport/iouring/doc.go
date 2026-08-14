// Package iouring implements engine/transport.Transport over a persistent
// per-peer TCP connection using a hand-rolled binary wire protocol
// (wire.go, frame.go), with all socket I/O driven by io_uring
// (infra/ioruntime) rather than blocking syscalls.
//
// # Scope of this pass
//
// Only Heartbeat is wired end-to-end: Client.Heartbeat sends a real
// MsgHeartbeatRequest frame over a real connection (conn.go), and
// Server's serverConn.dispatch decodes it, calls RequestHandler.Heartbeat,
// and sends back a real MsgHeartbeatResponse frame. The other five
// engine/transport.Transport RPCs already have working Marshal/Unmarshal
// pairs in wire.go, but Client's methods for them are thin stubs
// (contracts.ErrNotImplemented) and Server does not dispatch their
// message IDs at all yet — see each Client method's TODO in client.go.
// The pattern proven once by Heartbeat is mechanically identical for the
// rest.
//
// # Ownership contract: HandleCompletion
//
// Like infra/storage/journal.Store, none of Client, Server, conn, or
// serverConn can run an event loop themselves. Each only SUBMITS io_uring
// operations (through the ioruntime.Runtime passed to its constructor)
// and records a completion callback (or, for conn, a whole pending
// request) keyed by a userData value it minted. Something else — the
// reactor.Reactor that owns the same Runtime — must poll for completions
// and deliver them. The wiring contract:
//
//	client := iouring.NewClient(rt, r)
//	server := iouring.NewServer(rt, someHandler)
//	_ = server.Listen(addr)
//	r.SetEventHandler(func(ev reactor.Event) {
//		if client.HandleCompletion(ev) {
//			return
//		}
//		server.HandleCompletion(ev)
//	})
//	go r.Run()
//
// Every method on Client, Server, and their internal conn/serverConn
// types must therefore be called from that same reactor goroutine, per
// the single-goroutine discipline engine/reactor.Reactor documents.
//
// # userData encoding and multi-owner dispatch
//
// A Reactor's event handler is a single func(Event); when one Reactor
// eventually drives both a journal.Store and one or more of this
// package's Client/Server pairs (as the example above's HandleCompletion
// chain anticipates), completions from every owner arrive through that
// one func and must be routed to the right owner. This package's answer:
// every userData value it submits encodes its owning fd in the high 32
// bits and a small per-fd sequence number in the low 32 bits (sequence 0
// is always the persistent recv/accept; nonzero sequences are in-flight
// sends). HandleCompletion decodes the high bits first and immediately
// reports "not mine" (returns false) for any fd it does not own, letting
// a caller chain several HandleCompletion calls and stop at the first one
// that claims an event — the "return false and let the caller dispatch by
// range" option this package chose over a shared, centrally-allocated
// dispatch map, because it needs no coordination between independently
// constructed owners.
//
// This also happens to keep this package's userData values numerically
// far away from journal.Store's own userData counter, which starts at 1
// and increments by 1 per operation: a real fd is never 0, so the lowest
// value this package ever mints is 1<<32, a range journal.Store's counter
// would need billions of operations to reach. That is not a proof of
// non-collision, only a pragmatic one — the actual correctness property
// this design relies on is the fd-range check in HandleCompletion, not
// the numeric gap.
//
// # Heartbeat identity
//
// HeartbeatRequest (wire.go) carries no NodeID field, and
// RequestHandler.Heartbeat (server.go) takes no caller identity either.
// This is deliberate, not an oversight: engine/transport.Transport's
// Heartbeat contract is "ping node id for liveness" — id names the peer
// being asked, not the asker. A correct answer never depends on who is
// asking, only on the responder's own state, so there is nothing for the
// wire format or RequestHandler to carry. Adding a NodeID would require
// Client to know its own local node identity, which nothing in this
// package currently needs for any other reason, purely so a handler could
// ignore it.
//
// # Dialing
//
// conn.go's dialConn uses a plain blocking syscall.Connect rather than an
// io_uring connect operation. Dialing happens rarely — once per peer per
// reconnect — so blocking the reactor goroutine for the duration of one
// TCP handshake is an accepted, documented tradeoff rather than the
// always-async posture the rest of this package holds its I/O to.
package iouring
