package iouring

import (
	"fmt"
	"syscall"

	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
)

// RequestHandler supplies the behaviour behind each RPC a Server
// dispatches. Only Heartbeat exists on it in this pass: the other five
// engine/transport.Transport RPCs have no wire dispatch yet (see
// server.go's serverConn.dispatch), so adding stub methods for them here
// would only give every RequestHandler implementation unused no-ops to
// write. Extend this interface (and dispatch) together, once, when a
// later pass wires the next RPC.
//
// Heartbeat takes no caller identity on purpose: see doc.go's "Heartbeat
// identity" section for why HeartbeatRequest carries no NodeID field and
// this method needs none either.
type RequestHandler interface {
	// Heartbeat answers a liveness ping from a peer. The returned error,
	// if any, becomes the response's status code via StatusCodeFromError.
	Heartbeat() error
}

// Server accepts inbound peer connections and dispatches the requests
// they send to a RequestHandler, replying on the same connection. Every
// method, and every callback Server registers with rt, must run on the
// same reactor.Reactor goroutine that delivers HandleCompletion — the same
// single-goroutine discipline conn.go and infra/storage/journal.Store
// follow. Server has no need for a *reactor.Reactor reference itself
// (unlike Client): it never schedules a timer, since it only ever replies
// to a request already in hand.
type Server struct {
	rt      *ioruntime.Runtime
	handler RequestHandler

	listenFD int
	addr     string

	conns map[int]*serverConn // by connection fd
}

// NewServer creates a Server that submits io_uring operations through rt
// and dispatches decoded requests to handler.
func NewServer(rt *ioruntime.Runtime, handler RequestHandler) *Server {
	return &Server{
		rt:       rt,
		handler:  handler,
		listenFD: -1,
		conns:    make(map[int]*serverConn),
	}
}

// Listen binds and listens on addr and submits the first Accept. addr may
// use port 0 to let the kernel choose a port; call Addr afterward to
// learn what was actually bound.
func (s *Server) Listen(addr string) error {
	fd, err := listenTCP(addr)
	if err != nil {
		return err
	}
	bound, err := syscall.Getsockname(fd)
	if err != nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("iouring: getsockname: %w", err)
	}
	boundAddr, err := sockaddrToString(bound)
	if err != nil {
		_ = syscall.Close(fd)
		return err
	}

	s.listenFD = fd
	s.addr = boundAddr
	s.armAccept()
	return nil
}

// Addr returns the address Listen actually bound to.
func (s *Server) Addr() string {
	return s.addr
}

// HandleCompletion dispatches ev to whichever io_uring operation it
// completes: the listening socket's outstanding Accept, or one of this
// Server's accepted connections. It returns true if ev belonged to this
// Server. See doc.go for the userData encoding this relies on.
func (s *Server) HandleCompletion(ev reactor.Event) bool {
	fd, seq := splitUserData(ev.UserData)
	if fd == s.listenFD && seq == 0 {
		s.onAcceptCompletion(ev)
		return true
	}
	sc, ok := s.conns[fd]
	if !ok {
		return false
	}
	sc.handleCompletion(ev)
	return true
}

func (s *Server) armAccept() {
	if s.listenFD < 0 {
		return
	}
	// A submission failure here is rare enough (io_uring queue full) that
	// this pass has no observer to report it to; the listening socket
	// simply stops accepting, matching the accepted-limitation stance
	// infra/storage/journal's package doc takes for its own rare submit
	// failures.
	_ = s.rt.SubmitAccept(s.listenFD, makeUserData(s.listenFD, 0))
}

func (s *Server) onAcceptCompletion(ev reactor.Event) {
	defer s.armAccept() // keep listening regardless of this accept's outcome.
	if ev.Err != nil {
		return
	}
	connFD := int(ev.Result)
	sc := &serverConn{
		rt:      s.rt,
		fd:      connFD,
		recvBuf: make([]byte, recvBufSize),
		handler: s.handler,
		onDead:  func() { delete(s.conns, connFD) },
	}
	s.conns[connFD] = sc
	sc.armRecv()
}

// Close releases every resource this Server holds: the listening socket
// and every accepted connection's socket.
func (s *Server) Close() error {
	if s.listenFD >= 0 {
		_ = syscall.Close(s.listenFD)
		s.listenFD = -1
	}
	for fd, sc := range s.conns {
		sc.fail(errConnClosed)
		delete(s.conns, fd)
	}
	return nil
}

// serverConn is one inbound connection accepted by Server: it reads
// request frames (via the recvLoop helper conn.go also uses) and, for
// each one it understands, dispatches it to handler and sends the reply
// frame back on the same socket.
type serverConn struct {
	rt      *ioruntime.Runtime
	fd      int
	handler RequestHandler
	onDead  func() // removes this serverConn from its owning Server.conns.

	reassembler Reassembler
	recvBuf     []byte

	nextSendSeq uint64
	closed      bool
}

func (sc *serverConn) armRecv() {
	if sc.closed {
		return
	}
	if err := sc.rt.SubmitRecv(sc.fd, sc.recvBuf, makeUserData(sc.fd, 0)); err != nil {
		sc.fail(err)
	}
}

// handleCompletion routes a completion already established (by Server's
// caller, via HandleCompletion) to belong to sc: seq 0 is the persistent
// recv, anything else is a fire-and-forget response send whose only
// interesting outcome is failure (which kills the connection, since a
// half-written frame leaves the peer's Reassembler unable to make
// progress).
func (sc *serverConn) handleCompletion(ev reactor.Event) {
	if sc.closed {
		return
	}
	_, seq := splitUserData(ev.UserData)
	if seq == 0 {
		if err := recvLoop(sc.rt, sc.fd, sc.recvBuf, &sc.reassembler, ev, sc.dispatch); err != nil {
			sc.fail(err)
		}
		return
	}
	if ev.Err != nil {
		sc.fail(ev.Err)
	}
}

// dispatch decodes one complete request frame and, for a message ID this
// pass understands, invokes the handler and sends the reply frame back.
// An unrecognized message ID (any of the other five RPCs, not yet wired
// on the server side either) is silently dropped: no caller sends one
// yet, so there is nothing to answer.
func (sc *serverConn) dispatch(hdr FrameHeader, body []byte) {
	switch hdr.MessageID {
	case MsgHeartbeatRequest:
		var req HeartbeatRequest
		_ = req.Unmarshal(body) // HeartbeatRequest has no fields; never fails.

		err := sc.handler.Heartbeat()
		resp := HeartbeatResponse{Status: StatusCodeFromError(err)}
		respBody, mErr := resp.Marshal()
		if mErr != nil {
			return // HeartbeatResponse.Marshal never actually fails; guard anyway.
		}
		sc.reply(hdr.CorrelationID, MsgHeartbeatResponse, respBody)
	}
}

func (sc *serverConn) reply(correlationID uint64, msgID MessageID, body []byte) {
	frameBytes := EncodeFrame(uint16(msgID), wireSchemaVersion, correlationID, body)
	sc.nextSendSeq++
	if sc.nextSendSeq == 0 { // guard the reserved recv sequence (0) on wraparound.
		sc.nextSendSeq++
	}
	if err := sc.rt.SubmitSend(sc.fd, frameBytes, makeUserData(sc.fd, sc.nextSendSeq)); err != nil {
		sc.fail(err)
	}
}

// fail tears the connection down. err is not yet surfaced anywhere (this
// pass has no observer to report it to); the parameter is kept so a
// future logging/metrics hook has it available without changing every
// call site.
func (sc *serverConn) fail(err error) {
	if sc.closed {
		return
	}
	sc.closed = true
	_ = err
	_ = syscall.Close(sc.fd)
	if sc.onDead != nil {
		sc.onDead()
	}
}
